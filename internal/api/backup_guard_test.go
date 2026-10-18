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

// After a refused or empty restore the ten-minute slot is free, but the next
// attempt waits the short cooldown; a failure of ours (forget) does not.
func TestBackupGuardRefusalCooldown(t *testing.T) {
	ctx := context.Background()
	old := restoreRefusalCooldown
	restoreRefusalCooldown = 150 * time.Millisecond
	defer func() { restoreRefusalCooldown = old }()

	g := newBackupGuard(1, 10*time.Minute, time.Now)
	u, other := uuid.New(), uuid.New()
	rel, _, ok := g.acquire(ctx, u, 10*time.Minute)
	if !ok {
		t.Fatal("first attempt refused")
	}
	rel()
	g.forgetWithCooldown(u)
	if _, wait, ok := g.acquire(ctx, u, 10*time.Minute); ok || wait <= 0 || wait > 150*time.Millisecond {
		t.Fatalf("right after a refusal: ok=%v wait=%v, want refused for under 150ms", ok, wait)
	}
	if rel, _, ok := g.acquire(ctx, other, 10*time.Minute); !ok {
		t.Fatal("another user was held up by u's cooldown")
	} else {
		rel()
	}
	time.Sleep(200 * time.Millisecond)
	rel, _, ok = g.acquire(ctx, u, 10*time.Minute)
	if !ok {
		t.Fatal("still refused after the cooldown (the ten-minute slot must be free)")
	}
	rel()
	// Our own failure: no cooldown at all.
	g.forget(u)
	if rel, _, ok := g.acquire(ctx, u, 10*time.Minute); !ok {
		t.Error("a retry after our own failure was held back")
	} else {
		rel()
	}
}
