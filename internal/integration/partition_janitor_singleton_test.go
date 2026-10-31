//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// partitionJanitorLock is the janitor's lock key ("rv_parj").
const partitionJanitorLock int64 = 0x72765f7061726a

// The partition janitor's tick runs on one replica at a time: while another replica holds its lock a tick creates nothing (a
// partition dropped for the test stays dropped); once the lock is free the next tick makes it again.
func TestPartitionJanitor_OneReplicaAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	j := samples.NewPartitionJanitor(pool)
	// A month the lookahead covers, not yet there: 3 months out, detached and dropped.
	var name string
	if err := pool.QueryRowContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition(now() + interval '3 months')`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	exists := func() bool {
		var ok bool
		if err := pool.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if _, err := pool.ExecContext(ctx, `DROP TABLE `+name); err != nil {
		t.Fatal(err)
	}

	other, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := other.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, partitionJanitorLock).Scan(&got); err != nil || !got {
		t.Fatalf("take the lock: %v %v", got, err)
	}
	j.Tick(ctx)
	if exists() {
		t.Fatal("a tick ran while another replica held the janitor lock")
	}
	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, partitionJanitorLock); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	j.Tick(ctx)
	if !exists() {
		t.Error("the tick after the lock was freed did not create the partition")
	}
}
