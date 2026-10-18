//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminhealth"
	"github.com/apohor/rivolt/internal/db"
)

// The home summary's database section: leased counts live leases only,
// streaming counts vehicles whose newest state row is under 10 minutes old.
func TestAdminHealthVehicles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`SELECT rivolt_ensure_vehicle_state_partition(now())`)
	uid, err := db.EnsureUser(ctx, pool, "health")
	if err != nil {
		t.Fatal(err)
	}
	live, stale, quiet := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO vehicles (id, user_id, rivian_vehicle_id) VALUES ($1,$4,'rv-live'),($2,$4,'rv-stale'),($3,$4,'rv-quiet')`, live, stale, quiet, uid)
	exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source) VALUES ($1, now() - interval '2 minutes', $2, 't'), ($1, now() - interval '3 hours', $2, 't')`, live, uid)
	exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source) VALUES ($1, now() - interval '3 hours', $2, 't')`, stale, uid)
	exec(`INSERT INTO subscription_leases (vehicle_id, pod_id, expires_at) VALUES ('rv-live','p', now() + interval '1 minute'), ('rv-stale','p', now() + interval '1 minute'), ('rv-quiet','p', now() - interval '1 minute')`)

	v := adminhealth.New(adminhealth.Config{DB: pool}).Summary(ctx).Vehicles
	if v.Status != adminhealth.OK || v.Leased != 2 || v.Streaming != 1 {
		t.Errorf("vehicles = %+v, want ok, leased 2 (the expired lease does not count), streaming 1", v)
	}
}
