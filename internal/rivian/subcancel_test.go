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
