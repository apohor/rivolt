//go:build integration

package push

import (
	"errors"
	"strings"
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
	if _, err := pool.ExecContext(ctx, `UPDATE push_apns_activities SET started_at = now() - interval '13 hours' WHERE user_id = $1 AND token = $2`, a, act(1)); err != nil {
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
