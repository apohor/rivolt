package rivian

import (
	"context"
	"sync"
	"testing"
	"time"
)

// rolloutEvents collects what a monitor's rollout observer is told.
type rolloutEvents struct {
	mu  sync.Mutex
	got []string
}

func (r *rolloutEvents) observe(event, kind, source string) {
	r.mu.Lock()
	r.got = append(r.got, event+"/"+kind+"/"+source)
	r.mu.Unlock()
}

func (r *rolloutEvents) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// waitFor polls for n events (the split check runs off the frame path).
func (r *rolloutEvents) waitFor(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g := r.list(); len(g) >= n {
			return g
		}
		time.Sleep(5 * time.Millisecond)
	}
	return r.list()
}

const rolloutVID = "veh-roll"

var rolloutAt = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// rolloutMonitor is a pod's monitor on a clock pinned to the frames' time: the pod acquired the vehicle at minute 5 of the test timeline.
func rolloutMonitor(store LiveStateStore, ev *rolloutEvents, prevEnd func() (time.Time, bool)) *StateMonitor {
	m := NewStateMonitor(nil, nil)
	m.now = func() time.Time { return rolloutAt.Add(5 * time.Minute) }
	m.noteAcquiredLocked(rolloutVID)
	m.SetLiveStateStore(store)
	m.vehicleInfo[rolloutVID] = &Vehicle{ID: rolloutVID, Model: "R1T"}
	m.SetRolloutObserver(ev.observe)
	if prevEnd != nil {
		m.prevEnd = func(context.Context, string, string, string) (time.Time, bool) { return prevEnd() }
	}
	return m
}

func driveFrame(minute int) *State {
	return &State{VehicleID: rolloutVID, At: rolloutAt.Add(time.Duration(minute) * time.Minute), Gear: "D", SpeedKph: 80,
		BatteryLevelPct: 70 - float64(minute)/10, OdometerKm: 30000 + float64(minute), Latitude: 32.7, Longitude: -96.8}
}

func chargeFrame(minute int) *State {
	return &State{VehicleID: rolloutVID, At: rolloutAt.Add(time.Duration(minute) * time.Minute), Gear: "P", ChargerState: "charging_active",
		ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: 7.5, BatteryLevelPct: 20 + float64(minute)/10, OdometerKm: 30000}
}

// A drive that comes back from the Redis snapshot is counted as a snapshot rehydrate and nothing else.
func TestRolloutDriveFromSnapshot(t *testing.T) {
	ctx := context.Background()
	store := newMemLiveStateStore()
	owner := rolloutMonitor(store, &rolloutEvents{}, nil)
	for i := 0; i < 5; i++ {
		owner.applyVehicleStatePush(ctx, rolloutVID, driveFrame(i))
	}
	ev := &rolloutEvents{}
	peer := rolloutMonitor(store, ev, nil)
	peer.applyVehicleStatePush(ctx, rolloutVID, driveFrame(6))
	peer.applyVehicleStatePush(ctx, rolloutVID, driveFrame(7))
	if got := ev.waitFor(t, 1); len(got) != 1 || got[0] != "rehydrate/drive/snapshot" {
		t.Fatalf("events %v, want one rehydrate/drive/snapshot", got)
	}
}

// With no snapshot and a previous drive that ended two minutes before, the new drive is a split of it.
func TestRolloutDriveLostIsASplit(t *testing.T) {
	ctx := context.Background()
	ev := &rolloutEvents{}
	m := rolloutMonitor(newMemLiveStateStore(), ev, func() (time.Time, bool) { return rolloutAt.Add(4 * time.Minute), true })
	m.applyVehicleStatePush(ctx, rolloutVID, driveFrame(6))
	m.applyVehicleStatePush(ctx, rolloutVID, driveFrame(7))
	got := ev.waitFor(t, 2)
	if len(got) != 2 || got[0] != "rehydrate/drive/fresh" || got[1] != "split/drive/" {
		t.Fatalf("events %v, want rehydrate/drive/fresh then split/drive/", got)
	}
}

// A drive that begins at takeover with no previous drive nearby is just a new drive: nothing is counted.
func TestRolloutNewDriveIsNotASplit(t *testing.T) {
	ctx := context.Background()
	for name, prev := range map[string]func() (time.Time, bool){
		"previous drive an hour old": func() (time.Time, bool) { return rolloutAt.Add(-time.Hour), true },
		"no previous drive":          func() (time.Time, bool) { return time.Time{}, false },
	} {
		ev := &rolloutEvents{}
		m := rolloutMonitor(newMemLiveStateStore(), ev, prev)
		m.applyVehicleStatePush(ctx, rolloutVID, driveFrame(6))
		time.Sleep(100 * time.Millisecond)
		if got := ev.list(); len(got) != 0 {
			t.Errorf("%s: events %v, want none", name, got)
		}
	}
}

// A charge comes back from the snapshot, or is split when neither the snapshot nor the DB had it.
func TestRolloutCharge(t *testing.T) {
	ctx := context.Background()
	store := newMemLiveStateStore()
	owner := rolloutMonitor(store, &rolloutEvents{}, nil)
	for i := 0; i < 5; i++ {
		owner.applyVehicleStatePush(ctx, rolloutVID, chargeFrame(i))
	}
	ev := &rolloutEvents{}
	peer := rolloutMonitor(store, ev, nil)
	peer.applyVehicleStatePush(ctx, rolloutVID, chargeFrame(6))
	if got := ev.waitFor(t, 1); len(got) != 1 || got[0] != "rehydrate/charge/snapshot" {
		t.Fatalf("snapshot: events %v", got)
	}

	ev2 := &rolloutEvents{}
	lost := rolloutMonitor(newMemLiveStateStore(), ev2, func() (time.Time, bool) { return rolloutAt.Add(3 * time.Minute), true })
	lost.applyVehicleStatePush(ctx, rolloutVID, chargeFrame(6))
	if got := ev2.waitFor(t, 2); len(got) != 2 || got[1] != "split/charge/" {
		t.Fatalf("lost: events %v", got)
	}
}

// Each kind is decided once per ownership: later frames of the same drive add nothing.
func TestRolloutCountsOncePerOwnership(t *testing.T) {
	ctx := context.Background()
	store := newMemLiveStateStore()
	owner := rolloutMonitor(store, &rolloutEvents{}, nil)
	owner.applyVehicleStatePush(ctx, rolloutVID, driveFrame(0))
	ev := &rolloutEvents{}
	peer := rolloutMonitor(store, ev, nil)
	for i := 1; i < 10; i++ {
		peer.applyVehicleStatePush(ctx, rolloutVID, driveFrame(i))
	}
	time.Sleep(100 * time.Millisecond)
	if got := ev.list(); len(got) != 1 {
		t.Fatalf("events %v, want exactly one", got)
	}
}

// A vehicle that comes back to a pod that held it earlier in the same roll (A to B to A) is a new ownership with a new acquire time, and a
// drive lost on the way back is a split too.
func TestRolloutVehicleReturnsToTheSamePod(t *testing.T) {
	ctx := context.Background()
	ev := &rolloutEvents{}
	now := rolloutAt
	m := rolloutMonitor(newMemLiveStateStore(), ev, func() (time.Time, bool) { return rolloutAt.Add(14 * time.Minute), true })
	m.now = func() time.Time { return now }
	m.mu.Lock()
	m.acquiredAt[rolloutVID] = rolloutAt // first stay on this pod
	m.mu.Unlock()
	m.applyVehicleStatePush(ctx, rolloutVID, driveFrame(1))
	m.forgetVehicle(rolloutVID) // it moved to the peer
	m.SetLiveStateStore(newMemLiveStateStore()) // and the snapshot is gone when it comes back
	// Back at minute 15: a new acquire, no snapshot (Redis lost it), the previous drive ended at minute 14.
	now = rolloutAt.Add(15 * time.Minute)
	m.mu.Lock()
	m.noteAcquiredLocked(rolloutVID)
	m.mu.Unlock()
	m.applyVehicleStatePush(ctx, rolloutVID, driveFrame(16))
	if got := ev.waitFor(t, 2); len(got) != 2 || got[1] != "split/drive/" {
		t.Fatalf("events %v, want a fresh rehydrate and a split on the second ownership", got)
	}
}

// A real new drive that starts after a stop that ended well before the hand-off is not a split, even inside the 5 minute gap.
func TestRolloutStopEndedLongBeforeTheAcquireIsANewDrive(t *testing.T) {
	ctx := context.Background()
	ev := &rolloutEvents{}
	// Acquire at minute 5; the previous drive ended at minute 1 (4 minutes before the acquire); the new one starts at minute 6.
	m := rolloutMonitor(newMemLiveStateStore(), ev, func() (time.Time, bool) { return rolloutAt.Add(1 * time.Minute), true })
	m.applyVehicleStatePush(ctx, rolloutVID, driveFrame(6))
	time.Sleep(100 * time.Millisecond)
	if got := ev.list(); len(got) != 0 {
		t.Fatalf("events %v, want none: the stop ended before the hand-off", got)
	}
}
