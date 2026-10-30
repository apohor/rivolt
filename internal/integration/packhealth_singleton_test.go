//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/packhealth"
)

// backfillLock is packhealth's lock key ("rv_pkhl").
const backfillLock int64 = 0x72765f706b686c

// BackfillAll runs on one replica at a time: while another replica's pass holds the advisory lock,
// a second call does nothing (here: the out-of-range sample it would prune stays); once the lock is
// free, the next call runs.
func TestPackHealthBackfillAll_OneReplicaAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "packhealth-lock")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'rv-ph')`, uid)
	exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source)
		SELECT $1, id, 'ph-1', now() - interval '2 hours', now() - interval '1 hour', 'live' FROM vehicles WHERE rivian_vehicle_id = 'rv-ph'`, uid)
	exec(`INSERT INTO vehicle_pack_health_samples (vehicle_id, charge_id, at, pack_kwh_effective, soc_delta_pct, energy_delivered_kwh, derate_active)
		SELECT c.vehicle_id, c.id, now(), 10, 50, 5, false FROM charges c WHERE c.external_id = 'ph-1'`)
	samples := func() int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_pack_health_samples`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Another replica's pass is running: it holds the lock on its own session.
	other, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := other.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, backfillLock).Scan(&got); err != nil || !got {
		t.Fatalf("take the lock: %v %v", got, err)
	}
	st := packhealth.NewStore(pool)
	if n, err := st.BackfillAll(ctx); err != nil || n != 0 {
		t.Fatalf("while another replica runs: %d, %v; want 0, nil", n, err)
	}
	if n := samples(); n != 1 {
		t.Fatalf("the skipped pass touched the samples: %d rows, want the 1 seeded", n)
	}

	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, backfillLock); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	if _, err := st.BackfillAll(ctx); err != nil {
		t.Fatalf("after the other pass: %v", err)
	}
	if n := samples(); n != 0 {
		t.Errorf("the pass did not run once the lock was free: %d rows, want the out-of-range sample pruned", n)
	}
	// The lock is released after a pass: another replica can take it.
	if err := pool.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, backfillLock).Scan(&got); err != nil || !got {
		t.Errorf("lock still held after the pass: %v %v", got, err)
	}
}
