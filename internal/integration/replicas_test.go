//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// The N-instance rows of the scaling postmortem (infra #224): a change made on one
// replica reaches every replica. The assertions loop over all replicas, never "the
// other one", so they hold for any N; CI runs three. Install settings are in
// settings_sync_test.go and the Rivian account in internal/api.
const replicas = 3

func replicaDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return ctx, pool
}

func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("not within %s: %s", within, what)
}

// A pod is the real auth middleware over its own session store. Whatever one pod
// revokes, every pod must refuse on the next request: no pod may keep a verdict.
type sessionPod struct {
	store *sessions.Store
	h     http.Handler
}

func newSessionPod(t *testing.T, pool *sql.DB) *sessionPod {
	t.Helper()
	store, err := sessions.New(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithSessionStore(sessions.NewAuthAdapter(store)).AcceptBearer(true)
	return &sessionPod{store: store, h: svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uid, ok := auth.UserFromContext(r.Context()); ok && uid != uuid.Nil {
			_, _ = w.Write([]byte(uid.String()))
			return
		}
		http.Error(w, "no user", http.StatusUnauthorized)
	}))}
}

// whoAmI is the user id the pod resolves for the credential, or "" when refused.
func (p *sessionPod) whoAmI(cookie, bearer string) string {
	r := httptest.NewRequest(http.MethodGet, "/ping", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: cookie})
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		return ""
	}
	return w.Body.String()
}

// Revocation made on any one replica (sign out, revoke one device, revoke everything)
// is honoured by every replica on the very next request, for browser cookies and for
// the iPhone app's bearer alike, and leaves other users' sessions alone.
func TestSessionRevocationReachesEveryReplica(t *testing.T) {
	ctx, pool := replicaDB(t)
	var pods [replicas]*sessionPod
	for i := range pods {
		pods[i] = newSessionPod(t, pool)
	}
	alice, err := db.EnsureUser(ctx, pool, "replica-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "replica-bob")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(uid uuid.UUID, kind string) (sessions.Session, string) {
		s, raw, err := pods[0].store.Create(ctx, uid, time.Hour, sessions.CreateOpts{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		return s, raw
	}
	everyPod := func(what, cookie, bearer string, want uuid.UUID) {
		t.Helper()
		wantID := ""
		if want != uuid.Nil {
			wantID = want.String()
		}
		for i, p := range pods {
			if got := p.whoAmI(cookie, bearer); got != wantID {
				t.Errorf("%s: replica %d resolved %q, want %q", what, i, got, wantID)
			}
		}
	}

	_, bobWeb := mk(bob, sessions.KindWeb)
	_, webA := mk(alice, sessions.KindWeb)
	appSess, appA := mk(alice, sessions.KindApp)
	_, webB := mk(alice, sessions.KindWeb)
	_, appB := mk(alice, sessions.KindApp)
	everyPod("fresh web", webA, "", alice)
	everyPod("fresh app", "", appA, alice)

	// Sign out on replica 1 (RevokeByToken is what /api/auth/logout calls).
	if err := pods[1].store.RevokeByToken(ctx, webA); err != nil {
		t.Fatal(err)
	}
	everyPod("web after sign-out on replica 1", webA, "", uuid.Nil)

	// The user revokes one device from replica 2: the app token dies everywhere.
	if ok, err := pods[2].store.RevokeOwned(ctx, alice, appSess.ID); err != nil || !ok {
		t.Fatalf("RevokeOwned = %v, %v", ok, err)
	}
	everyPod("app after device revoke on replica 2", "", appA, uuid.Nil)
	everyPod("untouched web", webB, "", alice)
	everyPod("untouched app", "", appB, alice)

	// "Sign out everywhere" (account delete, lockout, admin reset) on replica 0.
	if n, err := pods[0].store.RevokeAll(ctx, alice); err != nil || n < 2 {
		t.Fatalf("RevokeAll = %d, %v", n, err)
	}
	everyPod("web after revoke-all", webB, "", uuid.Nil)
	everyPod("app after revoke-all", "", appB, uuid.Nil)
	everyPod("another user's session", bobWeb, "", bob)
}

// A user's own settings (here the receipts opt-in, a privacy choice) are read from
// the database on every use, so turning it off on one replica stops it on all.
func TestPerUserSettingsReachEveryReplica(t *testing.T) {
	ctx, pool := replicaDB(t)
	var fs [replicas]*settings.Factory
	for i := range fs {
		fs[i] = settings.NewFactory(pool)
	}
	alice, err := db.EnsureUser(ctx, pool, "replica-settings-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "replica-settings-bob")
	if err != nil {
		t.Fatal(err)
	}
	optIn := func(f *settings.Factory, uid uuid.UUID) bool { return f.RivianReceiptsOptIn(ctx, uid) }
	everyReplica := func(what string, uid uuid.UUID, want bool) {
		t.Helper()
		for i, f := range fs {
			if got := optIn(f, uid); got != want {
				t.Errorf("%s: replica %d opt-in = %v, want %v", what, i, got, want)
			}
		}
	}
	// Every replica builds (and so caches) the user's store before any change.
	everyReplica("before", alice, false)

	if err := settings.SetRivianReceiptsOptIn(ctx, fs[0].For(alice), true); err != nil {
		t.Fatal(err)
	}
	everyReplica("opted in on replica 0", alice, true)
	everyReplica("another user", bob, false)

	if err := settings.SetRivianReceiptsOptIn(ctx, fs[2].For(alice), false); err != nil {
		t.Fatal(err)
	}
	everyReplica("opted out on replica 2", alice, false)

	if err := fs[1].For(alice).Set(ctx, "charging.home_currency", "EUR"); err != nil {
		t.Fatal(err)
	}
	for i, f := range fs {
		all, err := f.For(alice).GetAll(ctx)
		if err != nil || all["charging.home_currency"] != "EUR" {
			t.Errorf("replica %d sees %q, %v; want EUR", i, all["charging.home_currency"], err)
		}
	}
	if err := fs[0].For(alice).Delete(ctx, "charging.home_currency"); err != nil {
		t.Fatal(err)
	}
	for i, f := range fs {
		if all, _ := f.For(alice).GetAll(ctx); all["charging.home_currency"] != "" {
			t.Errorf("replica %d still sees the deleted key: %q", i, all["charging.home_currency"])
		}
	}
}

// The kill switch tripped on one replica stops Rivian calls on every replica within the
// flags poll (10 s), and lifting it frees them again. This runs the real poll interval,
// so it is the bound the runbook promises.
func TestKillSwitchReachesEveryReplica(t *testing.T) {
	ctx, pool := replicaDB(t)
	var stores [replicas]*flags.Store
	for i := range stores {
		s, err := flags.OpenStore(ctx, pool, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		s.Start(ctx)
		stores[i] = s
	}
	all := func(paused bool) func() bool {
		return func() bool {
			for _, s := range stores {
				if s.KillSwitch().Paused != paused {
					return false
				}
			}
			return true
		}
	}
	if !all(false)() {
		t.Fatal("replicas should start unpaused")
	}

	// SetKillSwitch updates the writer's own snapshot at once; the others only learn
	// of it from the poll.
	if err := stores[0].SetKillSwitch(ctx, true, "upstream incident", "admin"); err != nil {
		t.Fatal(err)
	}
	// Another flag through the same refresh, so the shared path is covered, not one field.
	if err := stores[0].SetSignupCap(ctx, 7, "admin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, flags.DefaultPollInterval+5*time.Second, "every replica paused", all(true))
	waitFor(t, flags.DefaultPollInterval+5*time.Second, "every replica sees the signup cap", func() bool {
		for _, s := range stores {
			if s.SignupCap().Limit != 7 {
				return false
			}
		}
		return true
	})
	for i, s := range stores {
		if st := s.KillSwitch(); st.Reason != "upstream incident" || st.Actor != "admin" {
			t.Errorf("replica %d state = %+v", i, st)
		}
	}

	if err := stores[2].SetKillSwitch(ctx, false, "", "admin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, flags.DefaultPollInterval+5*time.Second, "every replica running again", all(false))
}
