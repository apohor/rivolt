//go:build integration

package demo

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// A demo loop's pass runs on one replica per tick: while another replica holds the job's lock the
// pass is skipped, the other jobs are not blocked by it, and once it is free the pass runs.
func TestOnOneReplica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	other, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended('rivolt-demo:weather', 0))`); err != nil {
		t.Fatal(err)
	}
	ran := map[string]int{}
	onOneReplica(ctx, pool, "weather", func() { ran["weather"]++ })
	onOneReplica(ctx, pool, "plans", func() { ran["plans"]++ })
	if ran["weather"] != 0 || ran["plans"] != 1 {
		t.Fatalf("while another replica holds weather: ran %v; want weather skipped, plans run", ran)
	}
	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtextextended('rivolt-demo:weather', 0))`); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	onOneReplica(ctx, pool, "weather", func() { ran["weather"]++ })
	onOneReplica(ctx, pool, "weather", func() { ran["weather"]++ }) // released after each pass
	if ran["weather"] != 2 {
		t.Errorf("after the lock was freed: weather ran %d times, want 2", ran["weather"])
	}
}
