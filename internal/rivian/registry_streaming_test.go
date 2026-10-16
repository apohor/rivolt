package rivian

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
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

// Independent charge-close side effects (push, receipts) all run for one
// close, in the order registered.
func TestComposeChargeCloseRunsEveryHook(t *testing.T) {
	if composeChargeClose(nil) != nil {
		t.Fatal("no hooks should compose to nil")
	}
	var calls []string
	h := func(name string) ChargeCloseHook {
		return func(_ context.Context, c charges.Charge) { calls = append(calls, name+":"+c.ID) }
	}
	one := composeChargeClose([]ChargeCloseHook{h("a")})
	one(context.Background(), charges.Charge{ID: "c1"})
	both := composeChargeClose([]ChargeCloseHook{h("a"), h("b")})
	both(context.Background(), charges.Charge{ID: "c2"})
	if got := strings.Join(calls, ","); got != "a:c1,a:c2,b:c2" {
		t.Errorf("calls = %s", got)
	}
}
