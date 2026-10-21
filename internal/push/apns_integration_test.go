//go:build integration

// The iPhone app's push against real Postgres: devices follow their app session,
// notification text is user-scoped and short-lived, and the fan-out sends Apple only
// an id.
//
// Run with: go test -tags=integration ./internal/push/...
package push

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

func apnsDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return ctx, pool
}

// session inserts a live session row for uid (the app session a device belongs to).
func session(t *testing.T, ctx context.Context, pool *sql.DB, uid uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, now() + interval '1 day')`,
		id, uid, []byte(id.String())); err != nil {
		t.Fatal(err)
	}
	return id
}

func tok(n int) string { return strings.Repeat(string(rune('a'+n)), 64) }

func TestAPNsDevicesFollowTheirSession(t *testing.T) {
	ctx, pool := apnsDB(t)
	uid, err := db.EnsureUser(ctx, pool, "apns-devices-a")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := db.EnsureUser(ctx, pool, "apns-devices-b")
	st, _ := OpenStore(pool, uid)
	stOther, _ := OpenStore(pool, other)
	live, dead, expired := session(t, ctx, pool, uid), session(t, ctx, pool, uid), session(t, ctx, pool, uid)
	for i, s := range []uuid.UUID{live, dead, expired} {
		if err := st.UpsertAPNsDevice(ctx, s, APNsDevice{Token: tok(i), Env: EnvSandbox, BundleID: "me.apoh.rivolt"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, dead); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired); err != nil {
		t.Fatal(err)
	}
	devs, err := st.ListLiveAPNsDevices(ctx)
	if err != nil || len(devs) != 1 || devs[0].Token != tok(0) {
		t.Fatalf("live devices = %+v, %v; want only the one on the live session", devs, err)
	}
	// New rows start with the web's defaults: two opt-ins off.
	p := devs[0].Prefs
	if !p.OnChargingDone || !p.OnAnomaly || !p.OnSoftwareUpdate || p.OnPlugInReminder || p.OnMilestones {
		t.Errorf("defaults = %+v", p)
	}
	// Re-registering never switches an opt-in on; only SetAPNsOptIns does, and only
	// for the owner.
	_ = st.UpsertAPNsDevice(ctx, live, APNsDevice{Token: tok(0), Env: EnvProduction, BundleID: "me.apoh.rivolt"})
	yes := true
	if ok, _ := stOther.SetAPNsOptIns(ctx, tok(0), &yes, &yes); ok {
		t.Error("another user changed a device's opt-ins")
	}
	if ok, _ := st.SetAPNsOptIns(ctx, tok(0), &yes, nil); !ok {
		t.Error("owner could not set the opt-in")
	}
	devs, _ = st.ListLiveAPNsDevices(ctx)
	if devs[0].Env != EnvProduction || !devs[0].Prefs.OnPlugInReminder || devs[0].Prefs.OnMilestones {
		t.Errorf("after re-register and opt-in: %+v", devs[0])
	}
	// The export lists devices without the token. (The revoked session's device is
	// already gone: revoking deletes it.)
	rows, err := st.ExportAPNsDevices(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("export = %+v, %v", rows, err)
	}
	// Deleting the session deletes its device; deleting the account deletes the rest.
	if _, err := pool.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, live); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM push_apns_devices WHERE user_id = $1`, uid).Scan(&n)
	if n != 1 {
		t.Errorf("after deleting a session: %d devices, want 1 (the expired session's)", n)
	}
	if _, err := st.CreateNotification(ctx, "charging_done", "t", "b", "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM push_apns_devices WHERE user_id = $1) + (SELECT count(*) FROM push_notifications WHERE user_id = $1)`, uid).Scan(&n)
	if n != 0 {
		t.Errorf("rows left after the account was deleted: %d", n)
	}
}

func TestNotificationsAreUserScopedAndShortLived(t *testing.T) {
	ctx, pool := apnsDB(t)
	a, _ := db.EnsureUser(ctx, pool, "apns-notif-a")
	b, _ := db.EnsureUser(ctx, pool, "apns-notif-b")
	sa, _ := OpenStore(pool, a)
	sb, _ := OpenStore(pool, b)
	id, err := sa.CreateNotification(ctx, "software_update", "Software update available", "Update 2026.40 available.", "https://evil.example/x")
	if err != nil {
		t.Fatal(err)
	}
	n, err := sa.GetNotification(ctx, id)
	if err != nil || n.Kind != "software_update" || n.Title != "Software update available" || n.URL != "/" {
		t.Fatalf("own notification = %+v, %v (an external url must have become /)", n, err)
	}
	if _, err := sb.GetNotification(ctx, id); err != ErrNotFound {
		t.Errorf("another user's id = %v, want ErrNotFound", err)
	}
	if _, err := sa.GetNotification(ctx, uuid.New()); err != ErrNotFound {
		t.Errorf("unknown id = %v", err)
	}
	if _, err := sa.CreateNotification(ctx, "bogus", "t", "b", "/"); err == nil {
		t.Error("an unknown kind was stored")
	}
	// Older than 24 h: not served, and the sweep removes it (and only it).
	old, _ := sa.CreateNotification(ctx, "anomaly", "old", "old", "/")
	if _, err := pool.ExecContext(ctx, `UPDATE push_notifications SET created_at = now() - interval '25 hours' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := sa.GetNotification(ctx, old); err != ErrNotFound {
		t.Errorf("expired notification = %v, want ErrNotFound", err)
	}
	if deleted, err := PurgeNotifications(ctx, pool); err != nil || deleted < 1 {
		t.Errorf("purge = %d, %v", deleted, err)
	}
	if _, err := sa.GetNotification(ctx, id); err != nil {
		t.Errorf("the fresh notification was purged: %v", err)
	}
}

// The fan-out sends Apple only an id, picks devices by their switches, removes a
// device Apple says is dead, ignores a signed-out one, and stores no text for a
// user with no app.
func TestFanAPNs(t *testing.T) {
	ctx, pool := apnsDB(t)
	uid, _ := db.EnsureUser(ctx, pool, "apns-fan-a")
	nobody, _ := db.EnsureUser(ctx, pool, "apns-fan-none")
	st, _ := OpenStore(pool, uid)
	stNone, _ := OpenStore(pool, nobody)
	sess := session(t, ctx, pool, uid)
	for i, env := range []string{EnvProduction, EnvProduction, EnvSandbox} {
		if err := st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: tok(i), Env: env, BundleID: "me.apoh.rivolt"}); err != nil {
			t.Fatal(err)
		}
	}
	// A device of another app (bundle) is never sent to.
	_ = st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: tok(5), Env: EnvProduction, BundleID: "com.example.other"})

	key, pemKey := testKey(t)
	var mu sync.Mutex
	sent := map[string]string{} // path -> body
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent[r.URL.Path] = string(b)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, tok(1)) { // this one is gone
			w.WriteHeader(410)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
		}
	})
	var logs bytes.Buffer
	svc := &Service{apns: a, logger: untimedLogger(&logs)}

	p := Payload{Title: "Charging complete", Body: "Added 31 kWh at Pasadena", URL: "/charges/abc", Kind: "charging_done"}
	if n := svc.fanAPNs(ctx, st, p); n != 2 {
		t.Fatalf("accepted by %d devices, want 2 (the dead one and the other app's are not)", n)
	}
	mu.Lock()
	if len(sent) != 3 { // tok0 production, tok1 production (dead), tok2 sandbox
		t.Errorf("Apple was asked %d times: %v", len(sent), sent)
	}
	for path, body := range sent {
		if strings.Contains(body, "Charging complete") || strings.Contains(body, "31 kWh") || strings.Contains(body, "charging_done") || strings.Contains(body, "/charges/abc") {
			t.Errorf("%s: the body carries notification content: %s", path, body)
		}
		if strings.Contains(path, tok(5)) {
			t.Error("a device registered for another app was sent to")
		}
	}
	mu.Unlock()
	devs, _ := st.ListLiveAPNsDevices(ctx)
	for _, d := range devs {
		if d.Token == tok(1) {
			t.Error("the device Apple reported unregistered is still stored")
		}
	}
	var texts int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM push_notifications WHERE user_id = $1 AND title = 'Charging complete' AND body = 'Added 31 kWh at Pasadena' AND url = '/charges/abc'`, uid).Scan(&texts)
	if texts != 1 {
		t.Errorf("stored notifications = %d, want one shared by all devices", texts)
	}
	// Switch-aware: a plug-in reminder is not wanted by anyone yet.
	if n := svc.fanAPNs(ctx, st, PlugInReminderPayload(30, 40)); n != 0 {
		t.Errorf("plug-in reminder went to %d devices with it off", n)
	}
	// A signed-out phone gets nothing.
	if _, err := pool.ExecContext(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, sess); err != nil {
		t.Fatal(err)
	}
	if n := svc.fanAPNs(ctx, st, p); n != 0 {
		t.Errorf("a revoked session's devices received %d pushes", n)
	}
	// What was logged names devices by id: no token, no notification text.
	for _, secret := range []string{tok(0), tok(1), tok(2), "Charging complete", "31 kWh", "/charges/abc"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log carries %q:\n%s", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "Unregistered") {
		t.Errorf("the dead device was not logged by reason:\n%s", logs.String())
	}
	// No app, no stored text.
	if n := svc.fanAPNs(ctx, stNone, p); n != 0 {
		t.Error("sent to a user with no devices")
	}
	var any int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM push_notifications WHERE user_id = $1`, nobody).Scan(&any)
	if any != 0 {
		t.Errorf("text stored for a user with no app: %d rows", any)
	}
}

// An Apple token is one install: registering it under a new account forgets the
// previous account's registration (a sign-out that never reached the server).
func TestAPNsTokenBelongsToTheLastAccountThatRegisteredIt(t *testing.T) {
	ctx, pool := apnsDB(t)
	a, _ := db.EnsureUser(ctx, pool, "apns-same-a")
	b, _ := db.EnsureUser(ctx, pool, "apns-same-b")
	sa, sb := session(t, ctx, pool, a), session(t, ctx, pool, b)
	stA, _ := OpenStore(pool, a)
	stB, _ := OpenStore(pool, b)
	reg := func(st *Store, sid uuid.UUID, token string) {
		t.Helper()
		if err := st.UpsertAPNsDevice(ctx, sid, APNsDevice{Token: token, Env: EnvSandbox, BundleID: "me.apoh.rivolt"}); err != nil {
			t.Fatal(err)
		}
	}
	reg(stA, sa, tok(10))
	reg(stA, sa, tok(11)) // A's other phone stays
	reg(stB, sb, tok(10)) // B signs in on A's phone
	if _, ok, _ := stA.APNsPrefs(ctx, tok(10)); ok {
		t.Error("A is still registered on a phone B signed in to")
	}
	if _, ok, _ := stB.APNsPrefs(ctx, tok(10)); !ok {
		t.Error("B's registration is missing")
	}
	if _, ok, _ := stA.APNsPrefs(ctx, tok(11)); !ok {
		t.Error("A's other phone was removed")
	}
}

// The user's own test notification reaches each signed-in iPhone whatever its
// switches say, sends Apple only an id, stores the fixed words (not anything a
// caller supplied), and does nothing for a user with no iPhone.
func TestSendAPNsTest(t *testing.T) {
	const testTok = "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"
	ctx, pool := apnsDB(t)
	uid, _ := db.EnsureUser(ctx, pool, "apns-test-a")
	nobody, _ := db.EnsureUser(ctx, pool, "apns-test-none")
	st, _ := OpenStore(pool, uid)
	stNone, _ := OpenStore(pool, nobody)
	sess := session(t, ctx, pool, uid)
	if err := st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: testTok, Env: EnvProduction, BundleID: "me.apoh.rivolt"}); err != nil {
		t.Fatal(err)
	}
	off := false
	if ok, err := st.SetAPNsPrefs(ctx, testTok, &off, &off, &off); err != nil || !ok {
		t.Fatal(ok, err)
	}
	key, pemKey := testKey(t)
	var mu sync.Mutex
	var bodies []string
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	})
	svc := &Service{apns: a, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if n := svc.SendAPNsTest(ctx, st); n != 1 {
		t.Fatalf("test reached %d devices, want 1 (switches are off, the test ignores them)", n)
	}
	mu.Lock()
	if len(bodies) != 1 || strings.Contains(bodies[0], "Notifications work") || strings.Contains(bodies[0], "Rivolt test") {
		t.Errorf("Apple was sent %v", bodies)
	}
	mu.Unlock()
	var texts int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM push_notifications WHERE user_id = $1 AND kind = 'test' AND title = 'Rivolt test'`, uid).Scan(&texts)
	if texts != 1 {
		t.Errorf("stored test notifications = %d, want 1", texts)
	}
	// The ordinary kinds still honour the switches.
	if n := svc.fanAPNs(ctx, st, Payload{Kind: "charging_done", Title: "t", Body: "b", URL: "/"}); n != 0 {
		t.Errorf("a charging-done push reached %d devices with it off", n)
	}
	if n := svc.SendAPNsTest(ctx, stNone); n != 0 {
		t.Errorf("test reached %d devices of a user with no iPhone", n)
	}
}

// untimedLogger logs without the time attribute. The no-leak log checks look for
// figures and tokens as substrings, and a timestamp's digits (22:50:11.4xx) can
// contain one by chance, which made them flaky.
func untimedLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
}
