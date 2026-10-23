//go:build integration

package leftopen

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The marker and the candidate query against real Postgres: one claim per episode, none within the cooldown of the
// vehicle's last, rows go with the vehicle and the account, only accounts with the alert on are candidates.
func TestMarkerAndCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	on, _ := db.EnsureUser(ctx, pool, "lo-on")
	off, _ := db.EnsureUser(ctx, pool, "lo-off")
	mkVeh := func(uid uuid.UUID, rid string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,$2,'R1S') RETURNING id`, uid, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	vOn, vOff := mkVeh(on, "lo-veh-on"), mkVeh(off, "lo-veh-off")
	// Only the account with a device that has the alert on is a candidate.
	if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, on_left_open) VALUES ($1,'https://fcm.googleapis.com/fcm/send/lo','p','a',TRUE)`, on); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES ($1,'https://fcm.googleapis.com/fcm/send/lo2','p','a')`, off); err != nil {
		t.Fatal(err)
	}
	src := NewSource(pool, nil, uuid.Nil)
	vs, err := src.Vehicles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []uuid.UUID
	for _, v := range vs {
		got = append(got, v.VehicleID)
	}
	if len(got) != 1 || got[0] != vOn || vs[0].Model != "R1S" || vs[0].RivianID != "lo-veh-on" {
		t.Fatalf("candidates = %+v, want only the opted-in account's truck %s (not %s)", vs, vOn, vOff)
	}

	// One claim per episode; none within the cooldown of the vehicle's last.
	start := time.Now().UTC().Truncate(time.Second)
	if ok, err := src.Claim(ctx, on, vOn, start); !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := src.Claim(ctx, on, vOn, start); ok {
		t.Error("the same episode was claimed twice")
	}
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(30*time.Minute)); ok {
		t.Error("a second episode was claimed within the cooldown of the last alert")
	}
	// Past the cooldown a new episode can be claimed.
	if _, err := pool.ExecContext(ctx, `UPDATE left_open_alerts SET sent_at = now() - interval '3 hours' WHERE vehicle_id = $1`, vOn); err != nil {
		t.Fatal(err)
	}
	if ok, _ := src.Claim(ctx, on, vOn, start.Add(4*time.Hour)); !ok {
		t.Error("an episode after the cooldown could not be claimed")
	}

	// The prune keeps recent rows and drops old ones.
	if _, err := pool.ExecContext(ctx, `UPDATE left_open_alerts SET sent_at = now() - interval '31 days' WHERE vehicle_id = $1 AND episode_started_at = $2`, vOn, start); err != nil {
		t.Fatal(err)
	}
	if n, err := src.Prune(ctx, time.Now().Add(-PruneAfter)); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	var left int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM left_open_alerts WHERE vehicle_id = $1`, vOn).Scan(&left)
	if left != 1 {
		t.Errorf("%d markers left, want 1", left)
	}

	// Deleting the vehicle removes its markers.
	if _, err := pool.ExecContext(ctx, `DELETE FROM vehicles WHERE id = $1`, vOn); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM left_open_alerts WHERE vehicle_id = $1`, vOn).Scan(&left)
	if left != 0 {
		t.Errorf("%d markers survived the vehicle", left)
	}
}
