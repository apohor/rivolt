//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// rivianPod is one replica: its own registry (per-user clients built the way cmd/rivolt
// builds them, from the shared database), its own syncer and its own status handler, all
// over one Postgres. Nothing is shared between pods except the database.
type rivianPod struct {
	reg    rivian.AccountRegistry
	raw    func(uuid.UUID) rivian.Account
	syncer *rivian.AccountSyncer
	status http.HandlerFunc
	// emails counts the raises that would send the re-auth email (the rising edge).
	emails *atomic.Int32
	// events is every call the pod's re-auth sink received, with what the guarded raise answered: the failure message of a test prints it,
	// so a flake names its cause (which pod raised, under which session version, and whether the row had already been flagged).
	mu     sync.Mutex
	events []string
}

func (p *rivianPod) record(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, time.Now().UTC().Format("15:04:05.000000")+" "+fmt.Sprintf(format, args...))
}

// diagnose is the state a failed re-auth assertion needs: the pod's sink calls, the user's flag columns, the stored session's version
// and the version this pod's client holds.
func (p *rivianPod) diagnose(ctx context.Context, pool *sql.DB, uid uuid.UUID) string {
	p.mu.Lock()
	events := append([]string(nil), p.events...)
	p.mu.Unlock()
	var needs bool
	var reason, at, notified, stored sql.NullString
	_ = pool.QueryRowContext(ctx, `SELECT needs_reauth, needs_reauth_reason, needs_reauth_at::text, needs_reauth_notified_at::text FROM users WHERE id = $1`, uid).
		Scan(&needs, &reason, &at, &notified)
	_ = pool.QueryRowContext(ctx, `SELECT updated_at::text FROM user_secrets WHERE user_id = $1 AND name = 'rivian.session'`, uid).Scan(&stored)
	held := "n/a"
	if lc, ok := p.raw(uid).(*rivian.LiveClient); ok {
		held = lc.SessionVersion().UTC().Format("2006-01-02 15:04:05.000000")
	}
	return fmt.Sprintf("\n  users: needs_reauth=%v reason=%q at=%s notified_at=%s\n  stored session updated_at=%s, this pod's client holds %s\n  sink calls on this pod:\n    %s",
		needs, reason.String, at.String, notified.String, stored.String, held, strings.Join(events, "\n    "))
}

func newRivianPod(ctx context.Context, pool *sql.DB, store *secrets.Store) *rivianPod {
	emails := &atomic.Int32{}
	pod := &rivianPod{emails: emails}
	reg := rivian.NewLiveAccountRegistry(func(uid uuid.UUID) *rivian.LiveClient {
		lc := rivian.NewLive()
		lc.WithReauthSink(func(sctx context.Context, reason string) {
			if reason == "" {
				_ = db.SetNeedsReauth(sctx, pool, uid, "")
				return
			}
			// As cmd/rivolt: only if the failing client's session is still the stored one.
			ver := rivian.SessionVersionFrom(sctx)
			tr, err := db.RaiseNeedsReauthIfCurrent(sctx, pool, uid, reason, ver)
			if tr {
				emails.Add(1)
			}
			pod.record("raise reason=%q under version %s -> transitioned=%v err=%v", reason, ver.UTC().Format("15:04:05.000000"), tr, err)
		})
		if needs, why, err := db.GetNeedsReauth(ctx, pool, uid); err == nil && needs {
			lc.SetNeedsReauth(true, why)
		}
		if sess, err := secrets.LoadRivianSession(ctx, store, uid); err == nil && sess.UserSessionToken != "" {
			lc.Restore(sess)
		}
		return lc
	})
	att := reg.(interface {
		AttachSyncer(*rivian.AccountSyncer)
		Raw(uuid.UUID) rivian.Account
	})
	syn := rivian.NewAccountSyncer(secrets.NewRivianAccountSource(pool, store), att.Raw, nil)
	att.AttachSyncer(syn)
	// The status handler gets no database handle on purpose: with one, it starts a background "lazy prime" that calls Rivian for an account
	// with no vehicles, and these tests use made-up tokens; the call would reach the real Rivian (and flag the user) at a time of its own.
	pod.reg, pod.raw, pod.syncer, pod.status = reg, att.Raw, syn, handleRivianStatus(reg, store, nil, nil, nil, nil)
	return pod
}

// get is GET /api/settings/rivian/ as the user, served by this pod.
func (p *rivianPod) get(ctx context.Context, t *testing.T, uid uuid.UUID) rivianStatusDTO {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/settings/rivian/", nil).WithContext(auth.WithUser(ctx, uid))
	w := httptest.NewRecorder()
	p.status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var got rivianStatusDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func (p *rivianPod) token(uid uuid.UUID) string { return p.raw(uid).Snapshot().UserSessionToken }

// With three replicas on one database, a re-link, a flag raised and a sign-out on one are
// seen by the others on their next request, for one user and not for another. This is
// the cross-pod staleness of the Oct 28 incident: a pod kept answering "needs reauth" (and
// kept the dead tokens) after the user signed in again on another pod.
func TestRivianAccountIsConsistentAcrossThreePods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})

	u1, err := db.EnsureUser(ctx, pool, freshName("pods-one"))
	if err != nil {
		t.Fatal(err)
	}
	u2, err := db.EnsureUser(ctx, pool, freshName("pods-two"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-180 * 24 * time.Hour)
	for _, u := range []uuid.UUID{u1, u2} {
		if err := secrets.SaveRivianSession(ctx, store, u, rivian.Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetNeedsReauth(ctx, pool, u, "session expired"); err != nil {
			t.Fatal(err)
		}
	}

	a, b, c := newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store)
	pods := map[string]*rivianPod{"A": a, "B": b, "C": c}
	for name, p := range pods {
		for _, u := range []uuid.UUID{u1, u2} {
			if s := p.get(ctx, t, u); !s.NeedsReauth || !s.Authenticated {
				t.Fatalf("pod %s should start with the dead, flagged session: %+v", name, s)
			}
		}
	}

	// 1. The user re-links on pod A: A holds the new session, stores it, clears the flag.
	// (Stamped a minute back so a sign-out a moment later is not mistaken, by design, for
	// the store lagging behind a sign-in made seconds ago on that pod.)
	now := rivian.Session{Email: "d@example.invalid", UserSessionToken: "new", AuthenticatedAt: time.Now().Add(-time.Minute)}
	a.raw(u1).Restore(now)
	a.raw(u1).(*rivian.LiveClient).SetNeedsReauth(false, "") // what Login does on the pod that signs in
	if err := secrets.SaveRivianSession(ctx, store, u1, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNeedsReauth(ctx, pool, u1, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		s := pods[name].get(ctx, t, u1) // the very next request on that pod
		if s.NeedsReauth || !s.Authenticated {
			t.Fatalf("after the re-link on A, pod %s says %+v", name, s)
		}
		if got := pods[name].token(u1); got != "new" {
			t.Fatalf("after the re-link on A, pod %s holds session %q, want new", name, got)
		}
	}
	// ...and the other user is untouched on every pod.
	for name, p := range pods {
		if s := p.get(ctx, t, u2); !s.NeedsReauth {
			t.Fatalf("pod %s: the other user's flag was cleared by someone else's re-link: %+v", name, s)
		}
		if got := p.token(u2); got != "old" {
			t.Fatalf("pod %s: the other user's session changed to %q", name, got)
		}
	}

	// 2. A pod with no web traffic (the lease holder) follows through the sweep alone.
	idle := newRivianPod(ctx, pool, store)
	idle.reg.For(u2) // loaded, then the world moves on
	now2 := rivian.Session{Email: "d@example.invalid", UserSessionToken: "newer", AuthenticatedAt: time.Now()}
	if err := secrets.SaveRivianSession(ctx, store, u2, now2); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNeedsReauth(ctx, pool, u2, ""); err != nil {
		t.Fatal(err)
	}
	idle.syncer.SyncAll(ctx, idle.reg.Loaded())
	if got := idle.token(u2); got != "newer" {
		t.Fatalf("the sweep did not bring the idle pod to the new session: %q", got)
	}
	if needs, _ := idle.raw(u2).NeedsReauth(); needs {
		t.Fatal("the sweep did not clear the idle pod's flag")
	}

	// 3. The reverse: a pod's client sees Rivian reject the session and raises the flag.
	if _, err := db.RaiseNeedsReauth(ctx, pool, u1, "session expired"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		if s := pods[name].get(ctx, t, u1); !s.NeedsReauth {
			t.Fatalf("pod %s did not see the flag raised elsewhere: %+v", name, s)
		}
	}
	if s := a.get(ctx, t, u2); s.NeedsReauth {
		t.Fatalf("raising one user's flag raised the other's: %+v", s)
	}

	// 4. Sign-out on pod C: the others drop the session.
	c.raw(u1).Logout()
	if err := secrets.SaveRivianSession(ctx, store, u1, rivian.Session{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		if s := pods[name].get(ctx, t, u1); s.Authenticated {
			t.Fatalf("pod %s still signed in after the sign-out on C: %+v", name, s)
		}
	}
	if got := a.token(u2); got != "newer" {
		t.Fatalf("the other user's session lost with someone else's sign-out: %q", got)
	}
}

// A3: right after a fresh sign-in on A, B still holds the old session (it has not synced
// yet) and Rivian rejects it. That rejection must not flag the user or send the email,
// and one against the stored session still must. The check is in the UPDATE itself, so a
// sign-in landing between B's read and write cannot lose.
func TestStalePodRejectionDoesNotReflagAFreshSignIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})
	uid, err := db.EnsureUser(ctx, pool, freshName("stale-pod"))
	if err != nil {
		t.Fatal(err)
	}
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer rejecting.Close()

	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: time.Now().Add(-179 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, b := newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store)
	a.get(ctx, t, uid)
	b.get(ctx, t, uid) // B has the old session and knows its stored version

	// The user signs in again on A.
	time.Sleep(50 * time.Millisecond)
	fresh := rivian.Session{Email: "d@example.invalid", UserSessionToken: "fresh", AuthenticatedAt: time.Now().Add(-time.Minute)}
	a.raw(uid).Restore(fresh)
	if err := secrets.SaveRivianSession(ctx, store, uid, fresh); err != nil {
		t.Fatal(err)
	}

	// B's stale client calls Rivian before its next sync (b.raw does not sync).
	lc := b.raw(uid).(*rivian.LiveClient).WithEndpoint(rejecting.URL + "/gateway")
	if _, err := lc.Vehicles(ctx); err == nil {
		t.Fatal("expected Rivian's 401")
	}
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); needs {
		t.Fatal("a stale pod's rejection flagged the user right after a fresh sign-in" + b.diagnose(ctx, pool, uid))
	}
	if n := b.emails.Load(); n != 0 {
		t.Fatalf("a stale pod's rejection would send %d re-auth email(s) right after a fresh sign-in%s", n, b.diagnose(ctx, pool, uid))
	}
	// B converges and, holding the fresh session, is then rejected for real: that flags once.
	if s := b.get(ctx, t, uid); s.NeedsReauth || b.token(uid) != "fresh" {
		t.Fatalf("B did not converge: %+v token=%q%s", s, b.token(uid), b.diagnose(ctx, pool, uid))
	}
	if _, err := b.raw(uid).(*rivian.LiveClient).WithEndpoint(rejecting.URL + "/gateway").Vehicles(ctx); err == nil {
		t.Fatal("expected Rivian's 401")
	}
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); !needs {
		t.Fatal("a rejection of the stored session must flag the user" + b.diagnose(ctx, pool, uid))
	}
	if n := b.emails.Load(); n != 1 {
		t.Fatalf("re-auth emails = %d, want 1%s", n, b.diagnose(ctx, pool, uid))
	}
	// Every pod then agrees.
	if s := a.get(ctx, t, uid); !s.NeedsReauth {
		t.Fatalf("pod A did not see the flag: %+v", s)
	}
}

// A stale client's rejection must not flag a user who disconnected or signed out elsewhere
// (the stored session is gone), and a client that does not yet know which stored session
// it holds raises nothing: the next sync records the version and the next rejection decides.
func TestStalePodRejectionAfterSignOutOrWithoutAVersionRaisesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer rejecting.Close()
	reject := func(p *rivianPod, uid uuid.UUID) {
		t.Helper()
		lc := p.raw(uid).(*rivian.LiveClient).WithEndpoint(rejecting.URL + "/gateway")
		if _, err := lc.Vehicles(ctx); err == nil {
			t.Fatal("expected Rivian's 401")
		}
	}

	// Signed out elsewhere: B holds a session with a known version, the stored row is deleted.
	uid, err := db.EnsureUser(ctx, pool, freshName("stale-signout"))
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "tok", AuthenticatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	b := newRivianPod(ctx, pool, store)
	b.get(ctx, t, uid) // loads it and records its version
	if v := b.raw(uid).(*rivian.LiveClient).SessionVersion(); v.IsZero() {
		t.Fatal("setup: B did not record the stored version")
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{}); err != nil { // the user disconnects on A
		t.Fatal(err)
	}
	reject(b, uid) // B's stale client, before its next sync
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); needs {
		t.Fatal("a stale pod flagged a user who had just disconnected")
	}
	if n := b.emails.Load(); n != 0 {
		t.Fatalf("a user who just disconnected would get %d reconnect email(s)", n)
	}

	// A client with no recorded version (restored, never synced): nothing is raised.
	uid2, err := db.EnsureUser(ctx, pool, freshName("no-version"))
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.SaveRivianSession(ctx, store, uid2, rivian.Session{Email: "d@example.invalid", UserSessionToken: "tok", AuthenticatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := newRivianPod(ctx, pool, store)
	c.reg.For(uid2) // builds the client, restores the session
	c.raw(uid2).(*rivian.LiveClient).SetSessionVersion(time.Time{})
	reject(c, uid2)
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid2); needs || c.emails.Load() != 0 {
		t.Fatalf("a client with no known session version raised the flag (needs=%v, emails=%d)", needs, c.emails.Load())
	}
	// The next sync records the version; the next rejection then does raise, once.
	c.get(ctx, t, uid2)
	reject(c, uid2)
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid2); !needs || c.emails.Load() != 1 {
		t.Fatalf("after the sync a rejection of the stored session must flag once (needs=%v, emails=%d)", needs, c.emails.Load())
	}
}

// The stored session's version is the database's clock and never goes backwards. A pod whose clock runs fast must not leave a version
// that a later, honest sign-in sorts below: a stale pod holding that earlier version would then pass the "stored <= loaded" check and
// flag (and email) a user who has just signed in again. Here the stored version is pushed 5 s into the future, as a fast pod would have
// written it; the next sign-in, from a pod with the correct clock, must still be newer, and the stale pod's rejection must raise nothing.
func TestSessionVersionNeverGoesBackwardsUnderClockSkew(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})
	uid, err := db.EnsureUser(ctx, pool, freshName("skewed-clocks"))
	if err != nil {
		t.Fatal(err)
	}
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer rejecting.Close()
	version := func() time.Time {
		t.Helper()
		var v time.Time
		if err := pool.QueryRowContext(ctx, `SELECT updated_at FROM user_secrets WHERE user_id = $1 AND name = 'rivian.session'`, uid).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Rapid writes are strictly increasing, even within one microsecond.
	var last time.Time
	for i := 0; i < 40; i++ {
		if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "t", AuthenticatedAt: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if v := version(); !v.After(last) {
			t.Fatalf("write %d: version %v is not after the previous %v", i, v, last)
		}
		last = version()
	}

	// A fast-clock pod's write (5 s ahead) is what the stale pod loads...
	if _, err := pool.ExecContext(ctx, `UPDATE user_secrets SET updated_at = clock_timestamp() + interval '5 seconds' WHERE user_id = $1 AND name = 'rivian.session'`, uid); err != nil {
		t.Fatal(err)
	}
	ahead := version()
	stale := newRivianPod(ctx, pool, store)
	stale.get(ctx, t, uid)
	if held := stale.raw(uid).(*rivian.LiveClient).SessionVersion(); !held.Equal(ahead) {
		t.Fatalf("the stale pod holds %v, want the stored %v", held, ahead)
	}
	// ...and the user then signs in again: the new version must be newer than that, whatever any pod's clock says.
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "fresh", AuthenticatedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if v := version(); !v.After(ahead) {
		t.Fatalf("a fresh sign-in got version %v, not after the earlier %v", v, ahead)
	}
	lc := stale.raw(uid).(*rivian.LiveClient).WithEndpoint(rejecting.URL + "/gateway")
	if _, err := lc.Vehicles(ctx); err == nil {
		t.Fatal("expected Rivian's 401")
	}
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); needs || stale.emails.Load() != 0 {
		t.Fatalf("a stale pod's rejection flagged a user who had just signed in again (needs=%v, emails=%d)%s", needs, stale.emails.Load(), stale.diagnose(ctx, pool, uid))
	}
}

// A pod whose clock runs 5 s AHEAD of the database signs the user in. Its session carries the pod's clock (AuthenticatedAt, 5 s in the
// future as the database sees it) while the stored version is the database's. The pod must still learn the stored version, from the
// version the save returned and, for a client that lost it, from the sync (same tokens, no clock comparison); and a stale pod that holds
// the earlier session must then be unable to flag the user.
func TestFastPodLearnsTheStoredVersionAndStalePodCannotFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})
	uid, err := db.EnsureUser(ctx, pool, freshName("fast-pod"))
	if err != nil {
		t.Fatal(err)
	}
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer rejecting.Close()
	stored := func() time.Time {
		t.Helper()
		var v time.Time
		if err := pool.QueryRowContext(ctx, `SELECT updated_at FROM user_secrets WHERE user_id = $1 AND name = 'rivian.session'`, uid).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fast, stale := newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store)
	fast.get(ctx, t, uid)
	stale.get(ctx, t, uid) // the stale pod holds the old session and its version

	// The user signs in on the fast pod: its client's session is stamped by the pod's clock, 5 s ahead of the database.
	signIn := rivian.Session{Email: "d@example.invalid", UserSessionToken: "fresh", AuthenticatedAt: time.Now().Add(5 * time.Second)}
	fast.raw(uid).Restore(signIn)
	ver, err := secrets.SaveRivianSessionVersioned(ctx, store, uid, signIn)
	if err != nil {
		t.Fatal(err)
	}
	if !ver.Equal(stored()) {
		t.Fatalf("the save returned %v, the stored version is %v", ver, stored())
	}
	learnStoredVersion(fast.raw(uid), ver)
	fast.get(ctx, t, uid) // the account syncer runs
	if held := fast.raw(uid).(*rivian.LiveClient).SessionVersion(); !held.Equal(stored()) {
		t.Fatalf("the fast pod holds %v, want the stored %v%s", held, stored(), fast.diagnose(ctx, pool, uid))
	}

	// A client that lost its version (a restored, never-synced one) learns it from the sync, with no clock comparison.
	fast.raw(uid).(*rivian.LiveClient).SetSessionVersion(time.Time{})
	fast.get(ctx, t, uid)
	if held := fast.raw(uid).(*rivian.LiveClient).SessionVersion(); !held.Equal(stored()) {
		t.Fatalf("a client that lost its version did not relearn it: holds %v, stored %v%s", held, stored(), fast.diagnose(ctx, pool, uid))
	}

	// A client with NO known version raises nothing, ever.
	fast.raw(uid).(*rivian.LiveClient).SetSessionVersion(time.Time{})
	if tr, err := db.RaiseNeedsReauthIfCurrent(ctx, pool, uid, "expired", time.Time{}); err != nil || tr {
		t.Fatalf("a zero held version raised the flag: %v %v", tr, err)
	}

	// The stale pod's 401 of the old session is recognised as stale and flags nobody.
	lc := stale.raw(uid).(*rivian.LiveClient).WithEndpoint(rejecting.URL + "/gateway")
	if _, err := lc.Vehicles(ctx); err == nil {
		t.Fatal("expected Rivian's 401")
	}
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); needs || stale.emails.Load() != 0 {
		t.Fatalf("a stale pod's rejection flagged a user who had just signed in on a fast pod (needs=%v, emails=%d)%s", needs, stale.emails.Load(), stale.diagnose(ctx, pool, uid))
	}
}

// THE interleaving behind the stale-pod false re-auth (a flake of the test above, and a real defect): pod B has a call in flight that
// used the OLD session. While it is in flight the user signs in again on pod A and B follows (new token, new version learnt). Then the old
// call's 401 arrives. It must not flag the user: the rejected call used the replaced session. Before the fix B attributed the rejection to
// whatever version it held when the error came back (the fresh one), so the raise passed its "is the stored session still the one I used"
// check and flagged a user whose fresh session was never rejected. Barriers, not timing: the server holds the old call until B has converged.
func TestInFlightCallOfAReplacedSessionCannotFlagTheFreshOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})
	uid, err := db.EnsureUser(ctx, pool, freshName("in-flight-replaced"))
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release // held until the test lets the old call's answer through; closed, later calls answer at once
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer rejecting.Close()

	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: time.Now().Add(-179 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, b := newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store)
	a.get(ctx, t, uid)
	b.get(ctx, t, uid) // B holds the old session and the old version
	bc := b.raw(uid).(*rivian.LiveClient)
	if bc.SessionVersionFor("old").IsZero() {
		t.Fatal("setup: B did not learn the old version for the old token")
	}

	// B's call with the OLD session goes out and is held at the server.
	bc.WithEndpoint(rejecting.URL + "/gateway")
	done := make(chan error, 1)
	go func() { _, err := bc.Vehicles(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("B's call never reached the server")
	}

	// While it is in flight the user signs in again on A, and B follows.
	fresh := rivian.Session{Email: "d@example.invalid", UserSessionToken: "fresh", AuthenticatedAt: time.Now().Add(-time.Minute)}
	a.raw(uid).Restore(fresh)
	if err := secrets.SaveRivianSession(ctx, store, uid, fresh); err != nil {
		t.Fatal(err)
	}
	if s := b.get(ctx, t, uid); s.NeedsReauth || b.token(uid) != "fresh" {
		t.Fatalf("B did not converge: %+v token=%q%s", s, b.token(uid), b.diagnose(ctx, pool, uid))
	}
	if bc.SessionVersionFor("fresh").IsZero() {
		t.Fatal("B converged without learning the fresh version")
	}
	if !bc.SessionVersionFor("old").IsZero() {
		t.Fatal("B still attributes the old token to a version after converging")
	}

	// Now the old call's 401 arrives.
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Rivian's 401 for the old call")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the held call never finished")
	}
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); needs || b.emails.Load() != 0 {
		t.Fatalf("a call made with the replaced session flagged a user whose fresh session was never rejected (needs=%v, emails=%d)%s", needs, b.emails.Load(), b.diagnose(ctx, pool, uid))
	}

	// B's local copy of the flag is put right by its next sync (the database is the truth), then B, holding the fresh session, is rejected
	// for real: that flags once.
	if s := b.get(ctx, t, uid); s.NeedsReauth {
		t.Fatalf("B's sync did not clear its local flag: %+v%s", s, b.diagnose(ctx, pool, uid))
	}
	if _, err := bc.Vehicles(ctx); err == nil {
		t.Fatal("expected Rivian's 401 for the fresh session")
	}
	if needs, _, _ := db.GetNeedsReauth(ctx, pool, uid); !needs || b.emails.Load() != 1 {
		t.Fatalf("a rejection of the fresh stored session must flag once (needs=%v, emails=%d)%s", needs, b.emails.Load(), b.diagnose(ctx, pool, uid))
	}
}

// freshName makes a username no earlier run (the CI repeats these tests 20 times on one database) has used, so each starts from an account with no flag
// and no stored session.
func freshName(base string) string { return base + "-" + uuid.NewString()[:8] }

// raceRounds is how many times each interleaving below is forced. The interleaving is deterministic (Postgres itself is the barrier), so one
// round proves it; more only show it is not flaky.
var raceRounds = 1

// A deterministic interleaving, no sleeps: a stale pod's rejection reaches the database WHILE a sign-in (or a sign-out) is still in its
// transaction. The sign-in (secrets.PutTxVersioned) writes the new session row (its version is the database's clock at that statement), then
// the user's row (users.rivian_signed_in_at, which locks it), then commits; a sign-out (secrets.DeleteTx) deletes the row and writes the user's
// row the same way. The stale raise (db.RaiseNeedsReauthIfCurrent) is guarded on "the stored session is still the one this client held". If
// the guard were evaluated on the snapshot the raise started with, it would still see the OLD committed session and pass; the raise would wait
// for the user's row and, after the commit, go through (Postgres rechecks only the target row after a lock wait, not the sessions table), flagging
// a user who has just signed in again (or out). The barrier is the raise's own backend showing as waiting on a Lock, and only then does the
// transaction commit. The user must not be flagged, and no email must follow.
func TestStaleRaiseRacingASignInOrOutCommitCannotFlagTheUser(t *testing.T) {
	for _, kind := range []string{"sign-in", "sign-out"} {
		for i := 0; i < raceRounds; i++ {
			t.Run(fmt.Sprintf("%s-%02d", kind, i), func(t *testing.T) { staleRaiseRace(t, kind) })
		}
	}
}

func staleRaiseRace(t *testing.T, kind string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})
	uid, err := db.EnsureUser(ctx, pool, freshName("race"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := secrets.SaveRivianSessionVersioned(ctx, store, uid, rivian.Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: time.Now().Add(-time.Hour)})
	if err != nil || old.IsZero() {
		t.Fatalf("old session: %v %v", old, err)
	}

	written := make(chan struct{})
	commit := make(chan struct{})
	txDone := make(chan error, 1)
	var newVersion time.Time // the sign-in's stored version (read after txDone)
	holdUserRow := func(ctx context.Context, tx *sql.Tx) error {
		// What the sign-in or sign-out does after the session row: the user's row, locked until commit.
		var at any
		if kind == "sign-in" {
			at = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET rivian_signed_in_at = $2 WHERE id = $1`, uid, at); err != nil {
			return err
		}
		close(written)
		select {
		case <-commit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() {
		if kind == "sign-in" {
			fresh, _ := json.Marshal(rivian.Session{Email: "d@example.invalid", UserSessionToken: "fresh", AuthenticatedAt: time.Now().Add(-time.Minute)})
			v, err := store.PutTxVersioned(ctx, uid, "rivian.session", fresh, holdUserRow)
			newVersion = v
			txDone <- err
			return
		}
		txDone <- store.DeleteTx(ctx, uid, "rivian.session", holdUserRow)
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the transaction never reached its barrier")
	}

	// The stale pod's rejection: its client holds `old`.
	type result struct {
		transitioned bool
		err          error
	}
	raised := make(chan result, 1)
	go func() {
		tr, err := db.RaiseNeedsReauthIfCurrent(ctx, pool, uid, "session expired", old)
		raised <- result{tr, err}
	}()
	// Barrier: wait until Postgres says the raise is blocked on the transaction's lock.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%needs_reauth = FALSE%' AND query LIKE '%users%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		select {
		case r := <-raised:
			t.Fatalf("the raise did not block on the transaction's row lock (answered %+v): the interleaving this test needs did not happen", r)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the raise never showed as waiting on a lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(commit) // the sign-in (or sign-out) commits: the stored session is now the fresh one (or gone)
	if err := <-txDone; err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	r := <-raised
	if r.err != nil {
		t.Fatalf("raise: %v", r.err)
	}
	needs, _, _ := db.GetNeedsReauth(ctx, pool, uid)
	if r.transitioned || needs {
		t.Fatalf("a stale pod's rejection that was waiting on a %s's lock flagged the user after it committed: transitioned=%v needs_reauth=%v", kind, r.transitioned, needs)
	}
	// The guard still does its job: a rejection by a client that holds the session that IS stored flags the user (a sign-in leaves the fresh one;
	// a sign-out leaves none, so there is nothing to flag, and a raise under the old version is still refused).
	if kind == "sign-in" {
		tr, err := db.RaiseNeedsReauthIfCurrent(ctx, pool, uid, "session expired", newVersion)
		if err != nil || !tr {
			t.Fatalf("a rejection of the stored session did not flag the user: transitioned=%v err=%v", tr, err)
		}
	} else if tr, err := db.RaiseNeedsReauthIfCurrent(ctx, pool, uid, "session expired", old); err != nil || tr {
		t.Fatalf("a rejection under a deleted session flagged the user: transitioned=%v err=%v", tr, err)
	}
}
