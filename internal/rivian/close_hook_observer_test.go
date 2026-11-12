package rivian

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/bg"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
)

// The fired counter ticks once per session that passes the gates: not for a repeat close, not under replay catch-up, not with no hook set.
func TestCloseHookObserverCountsFiredOnly(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	SetCloseHookObserver(func(typ string) { mu.Lock(); got[typ]++; mu.Unlock() })
	t.Cleanup(func() { SetCloseHookObserver(func(string) {}) })

	m := NewStateMonitor(nil, nil)
	m.driveCloseHook = func(context.Context, drives.Drive) {}
	m.chargeCloseHook = func(context.Context, charges.Charge) {}

	m.fireDriveCloseHook(drives.Drive{ID: "d1"})
	m.fireDriveCloseHook(drives.Drive{ID: "d1"}) // same session again
	m.fireChargeCloseHook(charges.Charge{ID: "c1"})
	quiet := NewStateMonitor(nil, nil)
	quiet.driveCloseHook = func(context.Context, drives.Drive) {}
	quiet.quietHooks.Store(true)
	quiet.fireDriveCloseHook(drives.Drive{ID: "d2"})                     // replay catch-up
	NewStateMonitor(nil, nil).fireDriveCloseHook(drives.Drive{ID: "d3"}) // no hook set
	bg.Default.Wait(5 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if got["drive"] != 1 || got["charge"] != 1 {
		t.Fatalf("fired = %v, want drive 1 and charge 1", got)
	}
}
