package rivian

import (
	"context"
	"testing"
	"time"
)

// seedVehicle fills every per-vehicle cache a streaming pod keeps.
func seedVehicle(m *StateMonitor, vehicleID string) {
	m.mu.Lock()
	m.cache[vehicleID] = &State{VehicleID: vehicleID, Gear: "P", Latitude: 34.16}
	m.stamp[vehicleID] = time.Now()
	m.lastSession[vehicleID] = &LiveSession{Active: true, TotalChargedEnergyKWh: 36.8}
	m.chargeBond[vehicleID] = "charge-1"
	m.lastSessionFor[vehicleID] = "charge-1"
	m.wsSeen[vehicleID] = true
	m.mu.Unlock()
	m.sessMu.Lock()
	m.sessions[vehicleID] = &liveSessions{drive: &liveDrive{}}
	m.rehydrated[vehicleID] = true
	m.sessMu.Unlock()
}

func subscribeForTest(m *StateMonitor, vehicleID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.active[vehicleID] = cancel
	m.activeCtx[vehicleID] = ctx
	m.mu.Unlock()
	return ctx, cancel
}

// A pod that loses a vehicle's lease forgets it, so a re-acquire seeds
// from REST and rehydrates the peer's snapshot instead of resuming its
// own last frame.
func TestUnsubscribeForgetsTheVehicle(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(newMemLiveStateStore())
	seedVehicle(m, "v")
	ctx, _ := subscribeForTest(m, "v")

	m.Unsubscribe("v")

	if ctx.Err() == nil {
		t.Error("Unsubscribe didn't cancel the run goroutine")
	}
	if st, _ := m.Latest("v"); st != nil {
		t.Error("cached State survived lease loss")
	}
	if m.LatestLiveSession("v") != nil {
		t.Error("live session survived lease loss")
	}
	m.mu.RLock()
	_, bond := m.chargeBond["v"]
	_, seen := m.wsSeen["v"]
	m.mu.RUnlock()
	if bond || seen {
		t.Errorf("charge bond %v / wsSeen %v survived lease loss", bond, seen)
	}
	m.sessMu.Lock()
	sess, rehydrated := m.sessions["v"], m.rehydrated["v"]
	m.sessMu.Unlock()
	if sess != nil || rehydrated {
		t.Errorf("accumulator %v / rehydrated %v survived: a re-acquire wouldn't rehydrate the peer's snapshot", sess != nil, rehydrated)
	}
}

// Without a shared store there's no peer snapshot to rebuild from, so the
// open drive/charge accumulators stay.
func TestUnsubscribeKeepsAccumulatorsWithoutAStore(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	seedVehicle(m, "v")
	subscribeForTest(m, "v")

	m.Unsubscribe("v")

	m.sessMu.Lock()
	sess := m.sessions["v"]
	m.sessMu.Unlock()
	if sess == nil || sess.drive == nil {
		t.Error("accumulator dropped with no shared store to rehydrate from")
	}
	if st, _ := m.Latest("v"); st != nil {
		t.Error("cached State survived lease loss")
	}
}

// release → re-acquire in quick succession: the old run goroutine exits
// after its successor started and must leave the successor's entry (and
// state) alone, or Unsubscribe could never cancel the successor.
func TestRunExitLeavesASuccessorAlone(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(newMemLiveStateStore())
	oldCtx, _ := subscribeForTest(m, "v")
	m.Unsubscribe("v")
	newCtx, _ := subscribeForTest(m, "v")
	seedVehicle(m, "v") // the successor's seed

	m.releaseActive(oldCtx, "v")

	m.mu.RLock()
	_, active := m.active["v"]
	owner := m.activeCtx["v"]
	m.mu.RUnlock()
	if !active || owner != newCtx {
		t.Fatal("the old goroutine's exit removed its successor's subscription")
	}
	if st, _ := m.Latest("v"); st == nil {
		t.Error("the old goroutine's exit forgot its successor's state")
	}

	m.Unsubscribe("v")
	if newCtx.Err() == nil {
		t.Error("Unsubscribe couldn't cancel the successor")
	}
}

// Frames recorded while the cancelled goroutine tears down, after
// Unsubscribe already forgot the vehicle, are forgotten again on exit.
func TestRunExitForgetsTeardownFrames(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(newMemLiveStateStore())
	ctx, _ := subscribeForTest(m, "v")
	m.Unsubscribe("v")
	seedVehicle(m, "v") // a late frame from the exiting goroutine

	m.releaseActive(ctx, "v")

	m.sessMu.Lock()
	sess := m.sessions["v"]
	m.sessMu.Unlock()
	if st, _ := m.Latest("v"); st != nil || sess != nil {
		t.Errorf("teardown frames survived the exit (state %v, accumulator %v)", st != nil, sess != nil)
	}
}

// A run that ends on its own (rejected session, not cancelled) frees its
// slot for a retry and keeps what it knew.
func TestRunExitWithoutCancelKeepsState(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(newMemLiveStateStore())
	ctx, _ := subscribeForTest(m, "v")
	seedVehicle(m, "v")

	m.releaseActive(ctx, "v")

	m.mu.RLock()
	_, active := m.active["v"]
	m.mu.RUnlock()
	if active {
		t.Error("an exited run kept its active entry; EnsureSubscribed could never retry")
	}
	if st, _ := m.Latest("v"); st == nil {
		t.Error("an uncancelled exit forgot the vehicle")
	}
}
