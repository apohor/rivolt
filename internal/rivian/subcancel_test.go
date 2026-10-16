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

// A subscription cancelled before it publishes its handle leaves the
// current one in place.
func TestSetSubCancelSkipsACancelledSubscription(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	liveCtx, liveCancel := context.WithCancelCause(context.Background())
	m.setSubCancel("v", liveCtx, liveCancel)
	deadCtx, deadCancel := context.WithCancelCause(context.Background())
	deadCancel(nil)
	m.setSubCancel("v", deadCtx, deadCancel)

	m.kickResubscribe("v", "test")
	if liveCtx.Err() == nil {
		t.Fatal("a cancelled subscription's handle replaced the live one")
	}
	m.mu.RLock()
	got := m.subCtx["v"]
	m.mu.RUnlock()
	if got != liveCtx {
		t.Error("subCtx points at the cancelled subscription")
	}
}
