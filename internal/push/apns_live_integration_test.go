//go:build integration

package push

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

func act(n int) string {
	return strings.Repeat(string(rune('a'+n)), 8) + strings.Repeat("0123456789abcdef", 4)
}

// The Live Activity registrations: off by default, only the user's own switch and
// vehicle, idempotent without extending the 12 hours, capped, forgotten with the
// switch, the device, the session and the sweep.
func TestLiveActivityRegistrations(t *testing.T) {
	ctx, pool := apnsDB(t)
	a, _ := db.EnsureUser(ctx, pool, "live-a")
	b, _ := db.EnsureUser(ctx, pool, "live-b")
	stA, _ := OpenStore(pool, a)
	stB, _ := OpenStore(pool, b)
	sa, sb := session(t, ctx, pool, a), session(t, ctx, pool, b)
	var va, vb uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'live-a-v') RETURNING id`, a).Scan(&va); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'live-b-v') RETURNING id`, b).Scan(&vb); err != nil {
		t.Fatal(err)
	}
	dev := func(st *Store, sid uuid.UUID, n int) string {
		token := strings.Repeat(string("0123456789abcdef"[n%16]), 64)
		if err := st.UpsertAPNsDevice(ctx, sid, APNsDevice{Token: token, Env: EnvSandbox, BundleID: "me.apoh.rivolt"}); err != nil {
			t.Fatal(err)
		}
		return token
	}
	dA := dev(stA, sa, 1)
	dB := dev(stB, sb, 2)

	// Off by default: a new device refuses, and says why.
	if on, ok, err := stA.APNsLive(ctx, dA); err != nil || !ok || on {
		t.Fatalf("default = %v %v %v, want off", on, ok, err)
	}
	if err := stA.RegisterActivity(ctx, dA, act(0), va); !errors.Is(err, ErrLiveOff) {
		t.Fatalf("switch off: %v, want ErrLiveOff", err)
	}
	// Another user's switch can't be set; the owner's can.
	if ok, _ := stB.SetAPNsLive(ctx, dA, true); ok {
		t.Error("another user turned on a device's Live Activity")
	}
	if ok, err := stA.SetAPNsLive(ctx, dA, true); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Not the user's device, not the user's vehicle.
	if err := stA.RegisterActivity(ctx, dB, act(0), va); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's device: %v", err)
	}
	if err := stA.RegisterActivity(ctx, dA, act(0), vb); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's vehicle: %v", err)
	}
	if n, _ := stA.ActivityCount(ctx); n != 0 {
		t.Fatalf("%d rows after refusals", n)
	}

	// Registered; again is the same row and keeps its start time.
	if err := stA.RegisterActivity(ctx, dA, act(0), va); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE push_apns_activities SET started_at = now() - interval '3 hours' WHERE user_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if err := stA.RegisterActivity(ctx, dA, act(0), va); err != nil {
		t.Fatal(err)
	}
	var age float64
	_ = pool.QueryRowContext(ctx, `SELECT extract(epoch FROM now() - started_at) FROM push_apns_activities WHERE user_id = $1`, a).Scan(&age)
	if n, _ := stA.ActivityCount(ctx); n != 1 || age < 3*3600-60 {
		t.Errorf("rows = %d, age = %.0fs: a re-registration must keep the start time", n, age)
	}

	// A token is one running activity: the last account to register it keeps it.
	if ok, _ := stB.SetAPNsLive(ctx, dB, true); !ok {
		t.Fatal("b's switch")
	}
	if err := stB.RegisterActivity(ctx, dB, act(0), vb); err != nil {
		t.Fatal(err)
	}
	if n, _ := stA.ActivityCount(ctx); n != 0 {
		t.Error("the previous account kept an activity token another account registered")
	}

	// Capped per user: the oldest go.
	for i := 1; i <= maxActivitiesPerUser+2; i++ {
		if err := stA.RegisterActivity(ctx, dA, act(i), va); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := stA.ActivityCount(ctx); n != maxActivitiesPerUser {
		t.Errorf("rows = %d, want the cap %d", n, maxActivitiesPerUser)
	}

	// End forgets one (and only the caller's); the sweep forgets stale ones.
	if err := stB.EndActivity(ctx, act(maxActivitiesPerUser+2)); err != nil {
		t.Fatal(err)
	}
	if n, _ := stA.ActivityCount(ctx); n != maxActivitiesPerUser {
		t.Error("another user's end removed the activity")
	}
	if err := stA.EndActivity(ctx, act(maxActivitiesPerUser+2)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE push_apns_activities SET started_at = now() - interval '13 hours' WHERE user_id = $1 AND token = $2`, a, act(3)); err != nil {
		t.Fatal(err)
	}
	if n, err := PurgeActivities(ctx, pool); err != nil || n < 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if n, _ := stA.ActivityCount(ctx); n != maxActivitiesPerUser-2 {
		t.Errorf("rows after end and sweep = %d, want %d", n, maxActivitiesPerUser-2)
	}

	// Switch off forgets the device's activities.
	if ok, err := stA.SetAPNsLive(ctx, dA, false); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if n, _ := stA.ActivityCount(ctx); n != 0 {
		t.Errorf("%d rows after the switch went off", n)
	}
	// Signing the phone out (revoking its session) removes its device and so its activities.
	if _, err := stA.SetAPNsLive(ctx, dA, true); err != nil {
		t.Fatal(err)
	}
	if err := stA.RegisterActivity(ctx, dA, act(9), va); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, sa); err != nil {
		t.Fatal(err)
	}
	if n, _ := stA.ActivityCount(ctx); n != 0 {
		t.Errorf("%d activities survived the session being revoked", n)
	}
}

// The sender: only activities whose device switch is on and whose app session is
// live; a routine update at most once per interval, the end always and then the row
// is gone; a dead token is forgotten; flag off sends nothing; the log holds no
// token and no figures.
func TestSendLive(t *testing.T) {
	ctx, pool := apnsDB(t)
	uid, _ := db.EnsureUser(ctx, pool, "live-send-a")
	st, _ := OpenStore(pool, uid)
	sess := session(t, ctx, pool, uid)
	var veh uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'live-send-v') RETURNING id`, uid).Scan(&veh); err != nil {
		t.Fatal(err)
	}
	const dev = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err := st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: dev, Env: EnvProduction, BundleID: "me.apoh.rivolt"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetAPNsLive(ctx, dev, true); err != nil || !ok {
		t.Fatal(ok, err)
	}
	live1, live2, dead := act(1), act(2), act(3)
	for _, tk := range []string{live1, live2, dead} {
		if err := st.RegisterActivity(ctx, dev, tk, veh); err != nil {
			t.Fatal(err)
		}
	}
	key, pemKey := testKey(t)
	var mu sync.Mutex
	hits := map[string]int{}
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, dead) {
			w.WriteHeader(410)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
		}
	})
	var logs bytes.Buffer
	svc := &Service{apns: a, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	state := NewLiveState(63, 11.4, 37)

	// Flag off: nothing, and the rows are untouched.
	if n := svc.UpdateLive(ctx, st, veh, state); n != 0 {
		t.Errorf("flag off: %d sent", n)
	}
	svc.WithLive(true)
	if n := svc.UpdateLive(ctx, st, veh, state); n != 2 {
		t.Fatalf("update reached %d, want 2 (the third is dead)", n)
	}
	if n, _ := st.ActivityCount(ctx); n != 2 {
		t.Errorf("rows = %d: the dead token should be forgotten", n)
	}
	// A second routine update inside the interval is held back; the end is not.
	if n := svc.UpdateLive(ctx, st, veh, state); n != 0 {
		t.Errorf("second update inside the interval reached %d", n)
	}
	if n := svc.EndLive(ctx, st, veh, state); n != 2 {
		t.Errorf("end reached %d, want 2", n)
	}
	if n, _ := st.ActivityCount(ctx); n != 0 {
		t.Errorf("rows after the end = %d", n)
	}
	mu.Lock()
	if hits["/3/device/"+live1] != 2 || hits["/3/device/"+live2] != 2 || hits["/3/device/"+dead] != 1 {
		t.Errorf("Apple saw %v", hits)
	}
	mu.Unlock()

	// Switch off, or a revoked session: nothing is sent.
	for _, tk := range []string{live1} {
		if ok, _ := st.SetAPNsLive(ctx, dev, true); !ok {
			t.Fatal("switch")
		}
		if err := st.RegisterActivity(ctx, dev, tk, veh); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE push_apns_activities SET last_sent_at = NULL WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE push_apns_devices SET on_charging_live = false WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if n := svc.UpdateLive(ctx, st, veh, state); n != 0 {
		t.Errorf("switch off: %d sent", n)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE push_apns_devices SET on_charging_live = true WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	// Another vehicle's activities are not touched.
	if n := svc.UpdateLive(ctx, st, uuid.New(), state); n != 0 {
		t.Errorf("another vehicle: %d sent", n)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE id = $1`, sess); err != nil {
		t.Fatal(err)
	}
	if n := svc.UpdateLive(ctx, st, veh, state); n != 0 {
		t.Errorf("expired session: %d sent", n)
	}
	// The log names activities by id: no token, no figures.
	for _, secret := range []string{live1, live2, dead, dev, "11.4", "soc_pct"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log carries %q:\n%s", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "Unregistered") {
		t.Errorf("the dead activity was not logged by reason:\n%s", logs.String())
	}
}

// The worker: a truck with a running activity that is charging gets its figures; one
// that is not charging gets nothing; the interval holds back a second tick; the
// charge-close hook sends the end with the final charge percent and forgets the
// activity. Trucks without an activity cost nothing.
func TestLiveWorkerAndChargeEnd(t *testing.T) {
	ctx, pool := apnsDB(t)
	uid, _ := db.EnsureUser(ctx, pool, "live-worker-a")
	st, _ := OpenStore(pool, uid)
	f := NewFactory(pool)
	sess := session(t, ctx, pool, uid)
	var charging, idle, noActivity uuid.UUID
	for rid, dst := range map[string]*uuid.UUID{"lw-charging": &charging, "lw-idle": &idle, "lw-none": &noActivity} {
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, rid).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	const dev = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := st.UpsertAPNsDevice(ctx, sess, APNsDevice{Token: dev, Env: EnvProduction, BundleID: "me.apoh.rivolt"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetAPNsLive(ctx, dev, true); err != nil || !ok {
		t.Fatal(ok, err)
	}
	actC, actI := act(4), act(5)
	if err := st.RegisterActivity(ctx, dev, actC, charging); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterActivity(ctx, dev, actI, idle); err != nil {
		t.Fatal(err)
	}

	key, pemKey := testKey(t)
	var mu sync.Mutex
	got := map[string][]string{} // path -> bodies
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = append(got[r.URL.Path], string(b))
		mu.Unlock()
	})
	svc := (&Service{apns: a, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).WithLive(true)
	asked := map[string]int{}
	src := LiveSourceFunc(func(u uuid.UUID, rid string) (float64, float64, float64, bool) {
		asked[rid]++
		if rid == "lw-charging" {
			return 63.6, 11.46, 41.7, true
		}
		return 0, 0, 0, false
	})

	if n := svc.TickLive(ctx, pool, f, src); n != 1 {
		t.Fatalf("tick reached %d, want 1 (only the charging truck)", n)
	}
	if asked["lw-none"] != 0 {
		t.Error("the worker looked at a truck with no activity")
	}
	mu.Lock()
	bodies := got["/3/device/"+actC]
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"soc_pct":64`) || !strings.Contains(bodies[0], `"power_kw":11.5`) || !strings.Contains(bodies[0], `"minutes_to_target":42`) || !strings.Contains(bodies[0], `"event":"update"`) {
		t.Errorf("update body = %v", bodies)
	}
	if len(got["/3/device/"+actI]) != 0 {
		t.Error("an idle truck was sent figures")
	}
	mu.Unlock()
	if n := svc.TickLive(ctx, pool, f, src); n != 0 {
		t.Errorf("a second tick inside the interval reached %d", n)
	}

	// The charge closes: the end carries the final percent and the activity is forgotten.
	if n := svc.EndLiveForCharge(ctx, f, uid, "lw-charging", 80.2); n != 1 {
		t.Fatalf("end reached %d, want 1", n)
	}
	mu.Lock()
	bodies = got["/3/device/"+actC]
	if len(bodies) != 2 || !strings.Contains(bodies[1], `"event":"end"`) || !strings.Contains(bodies[1], `"soc_pct":80`) || !strings.Contains(bodies[1], `"power_kw":0`) {
		t.Errorf("end body = %v", bodies)
	}
	mu.Unlock()
	if n, _ := st.ActivityCount(ctx); n != 1 {
		t.Errorf("rows after the end = %d, want 1 (the idle truck's)", n)
	}
	// A charge on a truck with no activity, or with the flag off, sends nothing.
	if n := svc.EndLiveForCharge(ctx, f, uid, "lw-none", 80); n != 0 {
		t.Errorf("end for a truck with no activity reached %d", n)
	}
	off := &Service{apns: a, logger: svc.logger}
	if n := off.TickLive(ctx, pool, f, src); n != 0 {
		t.Errorf("flag off: tick reached %d", n)
	}
	if n := off.EndLiveForCharge(ctx, f, uid, "lw-idle", 80); n != 0 {
		t.Errorf("flag off: end reached %d", n)
	}
}
