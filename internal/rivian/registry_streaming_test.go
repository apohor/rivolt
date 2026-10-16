package rivian

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Streaming is true when any local monitor runs the vehicle, not only
// its current owner's: an owner flip (users sharing a VIN) must not let
// the lease heal start a second subscription beside the old one. It
// does no database lookup, so a slow DB can't stall reconcile.
func TestRegistryStreamingAnyMonitor(t *testing.T) {
	r := &MonitorRegistry{monitors: map[uuid.UUID]*monitorEntry{}}
	oldOwner, newOwner := NewStateMonitor(nil, nil), NewStateMonitor(nil, nil)
	r.monitors[uuid.New()] = &monitorEntry{monitor: oldOwner}
	r.monitors[uuid.New()] = &monitorEntry{monitor: newOwner}

	if r.Streaming("vin-1") {
		t.Fatal("nothing runs vin-1")
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldOwner.mu.Lock()
	oldOwner.active["vin-1"] = cancel
	oldOwner.mu.Unlock()
	if !r.Streaming("vin-1") {
		t.Fatal("the old owner's run doesn't count: the heal would start a duplicate on the new owner")
	}
	if r.Streaming("vin-2") {
		t.Error("another vehicle reads as streaming")
	}
}
