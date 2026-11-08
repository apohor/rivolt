package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Without Redis the pod keeps its own claim: one attempt per user per window, never "ask every time", and it remembers how the attempt ended.
func TestVehicleFallbackGateWithoutRedisBoundsAttemptsPerPod(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	g := newVehicleFallbackGate(nil, "", nil, 5*time.Minute, nil)
	g.now = func() time.Time { return now }
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	if ok, _ := g.claim(ctx, a); !ok {
		t.Fatal("the first attempt for a user must be allowed")
	}
	if ok, prior := g.claim(ctx, a); ok || prior != "pending" {
		t.Errorf("second attempt while the first has not reported: %v %q, want refused and pending", ok, prior)
	}
	g.done(ctx, a, "empty")
	if ok, prior := g.claim(ctx, a); ok || prior != "empty" {
		t.Errorf("inside the window after an empty list: %v %q, want refused and empty", ok, prior)
	}
	if ok, _ := g.claim(ctx, b); !ok {
		t.Error("another user has their own window")
	}
	now = now.Add(5*time.Minute + time.Second)
	if ok, _ := g.claim(ctx, a); !ok {
		t.Error("after the window one more attempt is allowed")
	}
}
