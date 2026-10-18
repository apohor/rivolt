//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// The admin user list carries last_data_at, leased, receipts_opt_in and
// purge_warned_at; a user with none of them reads null/false.
func TestAdminUserRowStatusFields(t *testing.T) {
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

	busy, err := db.EnsureUser(ctx, pool, "busy")
	if err != nil {
		t.Fatal(err)
	}
	idle, err := db.EnsureUser(ctx, pool, "idle")
	if err != nil {
		t.Fatal(err)
	}
	// busy: two vehicles, the newer state row is on the second; a live lease
	// on the second; opted in to receipts; warned about inactivity.
	v1, v2 := uuid.New(), uuid.New()
	exec(`INSERT INTO vehicles (id, user_id, rivian_vehicle_id) VALUES ($1,$2,'rv-1'),($3,$2,'rv-2')`, v1, busy, v2)
	newest := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	for _, r := range []struct {
		v  uuid.UUID
		at time.Time
	}{{v1, newest.Add(-2 * time.Hour)}, {v1, newest.Add(-time.Hour)}, {v2, newest}} {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source) VALUES ($1,$2,$3,'test')`, r.v, r.at, busy)
	}
	exec(`INSERT INTO subscription_leases (vehicle_id, pod_id, expires_at) VALUES ('rv-2','pod-a', now() + interval '1 minute')`)
	exec(`INSERT INTO subscription_leases (vehicle_id, pod_id, expires_at) VALUES ('rv-1','pod-b', now() - interval '1 minute')`)
	exec(`INSERT INTO user_settings (user_id, value) VALUES ($1, '{"privacy.rivian_receipts":"1"}'::jsonb)`, busy)
	exec(`UPDATE users SET inactive_warned_at = now() - interval '3 days' WHERE id = $1`, busy)
	// idle: a vehicle with no state, an expired lease only, receipts not on.
	exec(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-3')`, idle)
	exec(`INSERT INTO subscription_leases (vehicle_id, pod_id, expires_at) VALUES ('rv-3','pod-c', now() - interval '1 hour')`)

	rows, err := db.ListUsersForAdmin(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	by := map[uuid.UUID]db.AdminUserRow{}
	for _, r := range rows {
		by[r.ID] = r
	}
	b, i := by[busy], by[idle]
	if b.LastDataAt == nil || !b.LastDataAt.Equal(newest) {
		t.Errorf("busy last_data_at = %v, want %v (newest across vehicles)", b.LastDataAt, newest)
	}
	if !b.Leased || !b.ReceiptsOptIn || b.PurgeWarnedAt == nil {
		t.Errorf("busy = leased %v, receipts %v, warned %v; want true, true, set", b.Leased, b.ReceiptsOptIn, b.PurgeWarnedAt)
	}
	if i.LastDataAt != nil || i.Leased || i.ReceiptsOptIn || i.PurgeWarnedAt != nil {
		t.Errorf("idle = %+v; want null/false everywhere (expired lease is not leased)", i)
	}
}
