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

// The gate's Redis names come from a purpose subkey, not the limiters' key itself: the same user under the same key names a different slot in the
// failed-login limiter than here, and no key means no Redis (per-pod claims only).
func TestVehicleFallbackKeyIsAPurposeSubkey(t *testing.T) {
	k := []byte("limiter-key-limiter-key-limiter-")
	sub := vehicleFallbackKey(k)
	if len(sub) != 32 || string(sub) == string(k) {
		t.Fatalf("subkey = %x, want 32 bytes that differ from the key", sub)
	}
	if string(vehicleFallbackKey(k)) != string(sub) || string(vehicleFallbackKey([]byte("other-key"))) == string(sub) {
		t.Error("the subkey must be stable for a key and differ between keys")
	}
	if vehicleFallbackKey(nil) != nil {
		t.Error("no key must stay no key")
	}
}
