package samples

import (
	"context"
	"testing"
	"time"
)

// TestNewPartitionJanitor_Defaults: the values picked in
// NewPartitionJanitor are the contract every operator runs
// against. `lookaheadMonths = 3` is what keeps the live
// recorder from ever writing into an unpartitioned range
// across long-running pods, and `interval = 1h` is what
// avoids the "deployed at 23:59 on the last day of the
// month" edge. Pin them so a quiet refactor can't silently
// regress either.
func TestNewPartitionJanitor_Defaults(t *testing.T) {
	j := NewPartitionJanitor(nil)
	if j == nil {
		t.Fatal("NewPartitionJanitor returned nil")
	}
	if j.lookaheadMonths != 3 {
		t.Errorf("lookaheadMonths = %d, want 3", j.lookaheadMonths)
	}
	if j.interval != time.Hour {
		t.Errorf("interval = %v, want 1h", j.interval)
	}
}

// TestEnsureLookahead_NilJanitor: a nil receiver is a
// legitimate "partitioning disabled" wiring — main only
// builds the janitor when running against Postgres. Calling
// EnsureLookahead on it must be a silent no-op so the
// startup path doesn't have to special-case dev/test.
func TestEnsureLookahead_NilJanitor(t *testing.T) {
	var j *PartitionJanitor
	if err := j.EnsureLookahead(context.Background()); err != nil {
		t.Fatalf("nil janitor: err = %v, want nil", err)
	}
}

// TestEnsureLookahead_NilDB: same intent but for the case
// where the janitor exists yet has no DB handle (we never
// build it that way today, but the guard exists and would
// otherwise panic in EnsureLookahead).
func TestEnsureLookahead_NilDB(t *testing.T) {
	j := &PartitionJanitor{db: nil, lookaheadMonths: 3, interval: time.Hour}
	if err := j.EnsureLookahead(context.Background()); err != nil {
		t.Fatalf("nil db: err = %v, want nil", err)
	}
}

// TestRun_NilJanitor: Run is launched as a top-level
// goroutine; if the nil-guard ever regresses, every test
// run would panic before the rest of the suite executed.
// Cheap insurance.
func TestRun_NilJanitor(t *testing.T) {
	var j *PartitionJanitor
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j.Run(ctx) // must return immediately, not panic
}

// TestRun_RespectsContextCancel: a non-nil janitor with a
// nil DB still has to honour ctx.Done() promptly. The
// initial EnsureLookahead is a no-op (nil DB), so Run
// should drop straight into the ticker loop and exit on
// the first ctx tick. We bound the test with a short
// deadline so a regression that swallows the cancel
// surfaces as a test timeout rather than a hung CI run.
func TestRun_RespectsContextCancel(t *testing.T) {
	j := &PartitionJanitor{db: nil, lookaheadMonths: 3, interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		j.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}
}
