//go:build integration

package tirelow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The marker and the candidate query against real Postgres: one claim per episode, none while a recent alert named every
// one of the wheels, an alert for a wheel no recent one named, rows go with the vehicle, only accounts with the alert on
// are candidates.
func TestMarkerAndCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	on, _ := db.EnsureUser(ctx, pool, "tl-on")
	off, _ := db.EnsureUser(ctx, pool, "tl-off")
	mkVeh := func(uid uuid.UUID, rid string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,$2,'R1S') RETURNING id`, uid, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	vOn, vOff := mkVeh(on, "tl-veh-on"), mkVeh(off, "tl-veh-off")
	if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, on_tire_low) VALUES ($1,'https://fcm.googleapis.com/fcm/send/tl','p','a',TRUE)`, on); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES ($1,'https://fcm.googleapis.com/fcm/send/tl2','p','a')`, off); err != nil {
		t.Fatal(err)
	}
	src := NewSource(pool, nil, uuid.Nil)
	vs, err := src.Vehicles(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].VehicleID != vOn || vs[0].Model != "R1S" || vs[0].RivianID != "tl-veh-on" {
		t.Fatalf("candidates = %+v, want only the opted-in account's truck %s (not %s)", vs, vOn, vOff)
	}
	// The dry run looks at every enabled account's truck, opted in or not.
	if all, err := src.Vehicles(ctx, true); err != nil || len(all) != 2 {
		t.Fatalf("dry-run candidates = %+v, %v; want both trucks", all, err)
	}

	start := time.Now().UTC().Truncate(time.Second)
	if ok, err := src.Claim(ctx, on, vOn, start, []string{"fl"}); !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := src.Claim(ctx, on, vOn, start, []string{"fl"}); ok {
		t.Error("the same episode was claimed twice")
	}
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(time.Hour), []string{"fl"}); ok {
		t.Error("the same wheel was claimed again within the cooldown")
	}
	// A wheel the last alert did not name alerts again inside the cooldown, and a set it already covered does not.
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(2*time.Hour), []string{"fl", "rr"}); !ok {
		t.Error("a second wheel could not be claimed inside the cooldown")
	}
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(3*time.Hour), []string{"rr"}); ok {
		t.Error("a wheel the last alert already named was claimed again")
	}
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(3*time.Hour), []string{"fl", "rr"}); ok {
		t.Error("a set the last alert already named was claimed again")
	}
	// Past the cooldown the same wheel can be claimed again.
	if _, err := pool.ExecContext(ctx, `UPDATE tire_low_alerts SET sent_at = now() - interval '8 days' WHERE vehicle_id = $1`, vOn); err != nil {
		t.Fatal(err)
	}
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(4*time.Hour), []string{"fl"}); !ok {
		t.Error("a claim after the cooldown could not be made")
	}

	// The prune keeps recent rows and drops old ones.
	if n, err := src.Prune(ctx, time.Now().Add(-PruneAfter)); err != nil || n != 0 {
		t.Fatalf("prune of recent rows = %d, %v", n, err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE tire_low_alerts SET sent_at = now() - interval '31 days' WHERE vehicle_id = $1 AND sent_at < now() - interval '1 day'`, vOn); err != nil {
		t.Fatal(err)
	}
	if n, err := src.Prune(ctx, time.Now().Add(-PruneAfter)); err != nil || n != 2 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	var left int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM tire_low_alerts WHERE vehicle_id = $1`, vOn).Scan(&left)
	if left != 1 {
		t.Errorf("%d markers left, want 1", left)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM vehicles WHERE id = $1`, vOn); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM tire_low_alerts WHERE vehicle_id = $1`, vOn).Scan(&left)
	if left != 0 {
		t.Errorf("%d markers survived the vehicle", left)
	}
}
