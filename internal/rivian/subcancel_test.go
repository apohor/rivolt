package rivian

import (
	"context"
	"testing"
)

// A subscription that ends after its successor started leaves the
// successor's cancel handle, so kickResubscribe can still nudge it.
func TestClearSubCancelLeavesTheSuccessor(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	oldCtx, oldCancel := context.WithCancelCause(context.Background())
	m.setSubCancel("v", oldCtx, oldCancel)
	newCtx, newCancel := context.WithCancelCause(context.Background())
	m.setSubCancel("v", newCtx, newCancel)

	m.clearSubCancel("v", oldCtx) // the old subscription returns late

	m.kickResubscribe("v", "test")
	if newCtx.Err() == nil {
		t.Fatal("the old subscription's exit removed its successor's handle: kickResubscribe couldn't reach it")
	}
	if oldCtx.Err() != nil {
		t.Error("kick reached the old subscription")
	}

	m.clearSubCancel("v", newCtx)
	m.mu.RLock()
	_, left := m.subCancel["v"]
	m.mu.RUnlock()
	if left {
		t.Error("a subscription's own exit didn't clear its handle")
	}
}

func TestSetSubCancelSkipsADoneContext(t *testing.T) {
	m := &StateMonitor{subCancel: map[string]context.CancelCauseFunc{}}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil) // the subscription died before it could be published
	m.setSubCancel("v", ctx, cancel)
	if _, ok := m.subCancel["v"]; ok {
		t.Fatal("a done subscription's cancel handle was published")
	}
	if _, ok := m.subCtx["v"]; ok {
		t.Fatal("a done subscription's context was published")
	}
	// A live one still publishes, and a later clear for the dead one
	// leaves it alone.
	live, liveCancel := context.WithCancelCause(context.Background())
	defer liveCancel(nil)
	m.setSubCancel("v", live, liveCancel)
	m.clearSubCancel("v", ctx)
	if _, ok := m.subCancel["v"]; !ok {
		t.Fatal("clearing the dead subscription removed the live one's handle")
	}
}
