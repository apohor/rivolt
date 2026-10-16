package rivian

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Losing the lease stops the vehicle's run on whichever monitor has it:
// after an owner flip that's the old owner's, which routing by owner
// left streaming on a pod without the lease. No database involved.
func TestRegistryUnsubscribeAnyMonitor(t *testing.T) {
	r := &MonitorRegistry{monitors: map[uuid.UUID]*monitorEntry{}}
	oldOwner, newOwner := NewStateMonitor(nil, nil), NewStateMonitor(nil, nil)
	r.monitors[uuid.New()] = &monitorEntry{monitor: oldOwner}
	r.monitors[uuid.New()] = &monitorEntry{monitor: newOwner}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldOwner.mu.Lock()
	oldOwner.active["vin-1"] = cancel
	oldOwner.mu.Unlock()

	r.Unsubscribe("vin-1")

	if ctx.Err() == nil {
		t.Fatal("the old owner's run still streams after the lease was lost")
	}
	oldOwner.mu.RLock()
	_, left := oldOwner.active["vin-1"]
	oldOwner.mu.RUnlock()
	if left {
		t.Error("the run's entry survived")
	}
}
