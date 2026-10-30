package rivian

import (
	"testing"
	"time"
)

// SessionActivity: an open drive or charge is open (whatever the power state); otherwise a vehicle
// whose latest state says sleep is asleep.
func TestSessionActivity(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	if open, asleep := m.SessionActivity("v"); open || asleep {
		t.Fatalf("no state: open=%v asleep=%v", open, asleep)
	}
	m.mu.Lock()
	m.cache["v"] = &State{VehicleID: "v", PowerState: "sleep"}
	m.stamp["v"] = time.Now()
	m.mu.Unlock()
	if open, asleep := m.SessionActivity("v"); open || !asleep {
		t.Errorf("asleep: open=%v asleep=%v", open, asleep)
	}
	m.sessMu.Lock()
	m.sessions["v"] = &liveSessions{charge: &liveCharge{}}
	m.sessMu.Unlock()
	if open, _ := m.SessionActivity("v"); !open {
		t.Error("open charge not reported")
	}
	m.sessMu.Lock()
	m.sessions["v"] = &liveSessions{drive: &liveDrive{}}
	m.sessMu.Unlock()
	m.mu.Lock()
	m.cache["v"] = &State{VehicleID: "v", PowerState: "go"}
	m.mu.Unlock()
	if open, asleep := m.SessionActivity("v"); !open || asleep {
		t.Errorf("open drive: open=%v asleep=%v", open, asleep)
	}
	m.sessMu.Lock()
	m.sessions["v"] = &liveSessions{}
	m.sessMu.Unlock()
	if open, asleep := m.SessionActivity("v"); open || asleep {
		t.Errorf("awake, no session: open=%v asleep=%v", open, asleep)
	}
}
