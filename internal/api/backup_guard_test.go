package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBackupGuardConcurrencyAndPerUserGap(t *testing.T) {
	ctx, gap := context.Background(), 10*time.Minute
	now := time.Unix(1_000_000, 0)
	g := newBackupGuard(2, 10*time.Minute, func() time.Time { return now })
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	relA, _, ok := g.acquire(ctx, a, gap)
	if !ok {
		t.Fatal("first backup refused")
	}
	// The same user again is refused until the gap passes, with the time left.
	if _, wait, ok := g.acquire(ctx, a, gap); ok || wait != 10*time.Minute {
		t.Fatalf("repeat: ok=%v wait=%v, want refused with 10m", ok, wait)
	}
	relB, _, ok := g.acquire(ctx, b, gap)
	if !ok {
		t.Fatal("second user refused with a free slot")
	}
	// Both slots taken: a third user is refused (retry soon), without being charged the gap.
	if _, wait, ok := g.acquire(ctx, c, gap); ok || wait <= 0 {
		t.Fatalf("third: ok=%v wait=%v, want refused", ok, wait)
	}
	relA()
	relC, _, ok := g.acquire(ctx, c, gap)
	if !ok {
		t.Fatal("third user refused after a slot freed (the refusal must not start their gap)")
	}
	relB()
	relC()

	now = now.Add(11 * time.Minute)
	if rel, _, ok := g.acquire(ctx, a, gap); !ok {
		t.Fatal("user refused after the gap passed")
	} else {
		rel()
	}
	if len(g.gaps.(*memGaps).last) > 1 {
		t.Errorf("old gap entries not forgotten: %d", len(g.gaps.(*memGaps).last))
	}
}

// A broken backup does not cost the user the wait.
func TestBackupGuardForget(t *testing.T) {
	ctx := context.Background()
	g := newBackupGuard(1, time.Hour, time.Now)
	u := uuid.New()
	rel, _, _ := g.acquire(ctx, u, time.Hour)
	rel()
	g.forget(u)
	if rel, _, ok := g.acquire(ctx, u, time.Hour); !ok {
		t.Error("retry refused after forget")
	} else {
		rel()
	}
}
