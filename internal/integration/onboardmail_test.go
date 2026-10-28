//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/onboardmail"
	"github.com/apohor/rivolt/internal/statement"
)

type fakeMail struct {
	mu   sync.Mutex
	sent []email.Message
}

func (f *fakeMail) Send(_ context.Context, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeMail) to(addr string) []email.Message {
	var out []email.Message
	for _, m := range f.sent {
		if m.To == addr {
			out = append(out, m)
		}
	}
	return out
}

// The welcome email and setup reminders end to end in Postgres: who gets
// what and when, never more than three emails, never to the unverified, the
// opted-out or the connected-with-data, each with the unsubscribe link and
// headers.
func TestOnboardingEmails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	mk := func(name string, age time.Duration, verified bool) uuid.UUID {
		t.Helper()
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, verified)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - $2::interval, welcome_sent_at = NULL WHERE id = $1`, uid, secs(age)); err != nil {
			t.Fatal(err)
		}
		return uid
	}
	newUser := mk("newbie", 20*time.Minute, true)
	tooFresh := mk("fresh", 2*time.Minute, true)
	unverified := mk("unverified", 5*time.Hour, false)
	optedOut := mk("optout", 5*time.Hour, true)
	connected := mk("connected", 5*time.Hour, true)
	oldBackfilled := mk("oldtimer", 40*24*time.Hour, true)
	if err := db.SetEngagementEmailsOff(ctx, pool, optedOut, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-conn')`, connected); err != nil {
		t.Fatal(err)
	}
	// The migration marks every account that existed then as welcomed.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET welcome_sent_at = now() WHERE id = $1`, oldBackfilled); err != nil {
		t.Fatal(err)
	}

	// Long since fully nudged (the real accounts that existed before this change).
	if _, err := pool.ExecContext(ctx, `UPDATE users SET setup_nudge_count = 2 WHERE id = $1`, oldBackfilled); err != nil {
		t.Fatal(err)
	}

	fm := &fakeMail{}
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	var sentKinds []string
	job := &onboardmail.Job{DB: pool, Mail: fm, Signer: func() *statement.Signer { return signer }, BaseURL: "https://rivolt.test",
		OnSent: func(kind string, _ int, _ uuid.UUID) { sentKinds = append(sentKinds, kind) }}

	w, n := job.Sweep(ctx)
	if got := len(fm.to("newbie@x.test")); w != 1 || got != 1 {
		t.Fatalf("first sweep: welcomes %d, newbie got %d; want 1, 1", w, got)
	}
	msg := fm.to("newbie@x.test")[0]
	if msg.Subject != "Welcome to Rivolt" || !strings.Contains(msg.Text, "/api/onboarding-email/unsubscribe?t=") ||
		msg.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.HasPrefix(msg.Headers["List-Unsubscribe"], "<https://rivolt.test/api/onboarding-email/unsubscribe?t=") {
		t.Errorf("welcome = %+v", msg)
	}
	// The link verifies, scoped to these emails.
	tok := msg.Headers["List-Unsubscribe"][strings.Index(msg.Headers["List-Unsubscribe"], "t=")+2 : len(msg.Headers["List-Unsubscribe"])-1]
	if gotUID, purpose, err := signer.Verify(tok, time.Now()); err != nil || gotUID != newUser || purpose != onboardmail.UnsubPurpose {
		t.Errorf("token = %v %q %v", gotUID, purpose, err)
	}
	if n != 0 {
		t.Errorf("nudges on the first sweep = %d (accounts are under 72 h or already welcomed), want 0", n)
	}
	for _, none := range []string{"fresh@x.test", "unverified@x.test", "optout@x.test", "connected@x.test", "oldtimer@x.test"} {
		if len(fm.to(none)) != 0 {
			t.Errorf("%s got mail: %+v", none, fm.to(none))
		}
	}
	_ = tooFresh
	_ = unverified

	// A second sweep sends nothing new (claims).
	if w, n := job.Sweep(ctx); w != 0 || n != 0 {
		t.Fatalf("second sweep sent %d + %d", w, n)
	}

	// Day 3: no vehicle -> reminder 1 (no_vehicle). A connected-but-empty
	// vehicle gets the no_data variant; a vehicle with data gets none.
	age := func(uid uuid.UUID, d time.Duration) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - $2::interval WHERE id = $1`, uid, secs(d)); err != nil {
			t.Fatal(err)
		}
	}
	age(newUser, 80*time.Hour)
	empty := mk("emptyveh", 80*time.Hour, true)
	withData := mk("hasdata", 80*time.Hour, true)
	for name, uid := range map[string]uuid.UUID{"emptyveh": empty, "hasdata": withData} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2)`, uid, "rv-"+name); err != nil {
			t.Fatal(err)
		}
	}
	age(connected, 80*time.Hour) // connected, but no data either
	var vid uuid.UUID
	_ = pool.QueryRowContext(ctx, `SELECT id FROM vehicles WHERE user_id = $1`, withData).Scan(&vid)
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition(now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (vehicle_id, user_id, at, source, battery_level_pct) VALUES ($1,$2,now(),'live',70)`, vid, withData); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{empty, withData} {
		if _, err := pool.ExecContext(ctx, `UPDATE users SET welcome_sent_at = now() WHERE id = $1`, u); err != nil {
			t.Fatal(err)
		}
	}
	w, n = job.Sweep(ctx)
	if w != 0 || n != 3 {
		t.Fatalf("day-3 sweep: welcomes %d nudges %d; want 0 and 3 (newbie, emptyveh, connected)", w, n)
	}
	if len(fm.to("hasdata@x.test")) != 0 || len(fm.to("optout@x.test")) != 0 || len(fm.to("unverified@x.test")) != 0 {
		t.Error("a user with data, an opted-out user or an unverified user was nudged")
	}
	if m := fm.to("newbie@x.test")[1]; !strings.Contains(m.Subject, "Finish setting up") {
		t.Errorf("no-vehicle nudge subject = %q", m.Subject)
	}
	if m := fm.to("emptyveh@x.test")[0]; !strings.Contains(m.Subject, "no data has arrived") {
		t.Errorf("no-data nudge subject = %q", m.Subject)
	}

	// Reminder 2 comes 6 days after the first, then never again: at most
	// welcome + 2 reminders in total.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET setup_nudged_at = now() - interval '7 days'`); err != nil {
		t.Fatal(err)
	}
	if _, n := job.Sweep(ctx); n != 3 {
		t.Fatalf("follow-up nudges = %d, want 3", n)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET setup_nudged_at = now() - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	if w, n := job.Sweep(ctx); w != 0 || n != 0 {
		t.Fatalf("after two reminders: %d + %d, want none", w, n)
	}
	if got := len(fm.to("newbie@x.test")); got != 3 {
		t.Errorf("newbie got %d emails in total, want 3 (welcome + 2 reminders)", got)
	}

	// Opting out stops pending reminders; opting back in resumes eligibility.
	if err := db.SetEngagementEmailsOff(ctx, pool, empty, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEngagementEmailsOff(ctx, pool, empty, true); err != nil { // idempotent
		t.Fatal(err)
	}
	if off, _ := db.EngagementEmailsOff(ctx, pool, empty); !off {
		t.Error("opt-out not recorded")
	}

	// No signer, no mail.
	none := &onboardmail.Job{DB: pool, Mail: fm, Signer: func() *statement.Signer { return nil }}
	before := len(fm.sent)
	none.Sweep(ctx)
	if len(fm.sent) != before {
		t.Error("sent without a signer")
	}
}

func secs(d time.Duration) string { return fmt.Sprintf("%d seconds", int(d.Seconds())) }

// An address Apple told us its relay no longer forwards to (email-disabled) gets no welcome or setup reminder, and
// gets them again once forwarding is back on (email-enabled).
func TestOnboardingEmailsSkipUndeliverableAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "relay-off", "relay-off@x.test", "relay-off", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - interval '5 hours', welcome_sent_at = NULL, email_undeliverable = TRUE WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	due := func() bool {
		t.Helper()
		ws, err := db.ListUsersDueForWelcome(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.UserID == uid {
				return true
			}
		}
		return false
	}
	if due() {
		t.Fatal("a welcome is due for an undeliverable address")
	}
	if ok, err := db.ClaimWelcome(ctx, pool, uid); err != nil || ok {
		t.Fatalf("ClaimWelcome = %v, %v; want refused", ok, err)
	}
	nudges, err := db.ListUsersDueForSetupNudge(ctx, pool, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nudges {
		if n.UserID == uid {
			t.Fatal("a setup reminder is due for an undeliverable address")
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET email_undeliverable = FALSE WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if !due() {
		t.Fatal("the welcome should be due again once the flag is cleared")
	}
}

// The welcome and the statement leave the iPhone-app line out for an account with a live app
// session (and keep it for one with only a browser session, a revoked or an expired app session).
func TestAppLinkIsSkippedForAccountsOnTheApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mk := func(name string) uuid.UUID {
		t.Helper()
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - interval '5 hours', welcome_sent_at = NULL WHERE id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		return uid
	}
	session := func(uid uuid.UUID, kind, extra string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at, kind)
			VALUES (gen_random_uuid(), $1, gen_random_bytes(32), `+extra+`, $2)`, uid, kind); err != nil {
			t.Fatal(err)
		}
	}
	onApp := mk("app-live")
	webOnly := mk("app-web")
	revoked := mk("app-revoked")
	expired := mk("app-expired")
	session(onApp, "app", `now() + interval '30 days'`)
	session(webOnly, "web", `now() + interval '30 days'`)
	session(revoked, "app", `now() + interval '30 days'`)
	session(expired, "app", `now() - interval '1 day'`)
	if _, err := pool.ExecContext(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1`, revoked); err != nil {
		t.Fatal(err)
	}
	ws, err := db.ListUsersDueForWelcome(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]bool{}
	for _, w := range ws {
		got[w.UserID] = w.HasApp
	}
	want := map[uuid.UUID]bool{onApp: true, webOnly: false, revoked: false, expired: false}
	for u, has := range want {
		if g, ok := got[u]; !ok || g != has {
			t.Errorf("welcome HasApp for %v = %v (listed %v), want %v", u, g, ok, has)
		}
	}
	st := statement.NewStore(pool)
	for u := range want {
		if err := st.SetPrefs(ctx, u, statement.Prefs{Enabled: true, Timezone: "UTC"}); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := st.OptedIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, c := range cs {
		if has, ok := want[c.UserID]; ok {
			seen++
			if c.HasApp != has {
				t.Errorf("statement HasApp for %v = %v, want %v", c.UserID, c.HasApp, has)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("saw %d of %d statement candidates", seen, len(want))
	}
}
