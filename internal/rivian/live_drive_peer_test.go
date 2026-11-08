package rivian

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"
)

// Single-owner M3-5b (inventory row 8, T20): /api/live-drive answers the same on every pod. The route used to read the drive accumulator in the
// monitor's memory, which only the pod that holds the vehicle's lease has; any other pod answered 204 whatever the truck was doing. A pod that
// does not stream the vehicle reads the owner's published accumulator (the live-state snapshot), the way peerChargeNow does for charges.

type driveRig struct {
	owner, peer *StateMonitor
	store       *memLiveStateStore
	at          time.Time
	minutes     int
}

// newDriveRig has an owner pod that has recorded `minutes` of a drive (72 km/h, about 1.2 km a minute) and a peer that shares its store.
func newDriveRig(t *testing.T, minutes int) *driveRig {
	t.Helper()
	ctx := context.Background()
	r := &driveRig{store: newMemLiveStateStore(), at: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), minutes: minutes}
	mk := func() *StateMonitor {
		m := NewStateMonitor(nil, nil)
		m.SetLiveStateStore(r.store)
		m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T", PackKWh: 135}
		m.now = func() time.Time { return r.at.Add(time.Duration(r.minutes) * time.Minute) }
		return m
	}
	r.owner, r.peer = mk(), mk()
	odo := 30000.0
	for minute := 0; minute <= minutes; minute++ {
		odo += 1.2
		r.owner.applyVehicleStatePush(ctx, chargeTestVehicle, &State{VehicleID: chargeTestVehicle, At: r.at.Add(time.Duration(minute) * time.Minute),
			Gear: "D", SpeedKph: 72, PowerState: "go", BatteryLevelPct: 80 - float64(minute)/4, OdometerKm: odo,
			Latitude: 30.27 + float64(minute)*0.001, Longitude: -97.74})
	}
	return r
}

func TestPeerPodSeesTheOwnersOpenDrive(t *testing.T) {
	r := newDriveRig(t, 20)
	own := r.owner.ActiveDrive(chargeTestVehicle)
	if own == nil || own.DistanceMi < 10 || own.SoCUsedPct < 4 {
		t.Fatalf("setup: the owner has %+v, want a 20-minute drive of about 15 miles", own)
	}
	peer := r.peer.ActiveDrive(chargeTestVehicle)
	if peer == nil {
		t.Fatal("a pod that does not hold the lease answered no drive while the owner is mid-drive (the 204 bug)")
	}
	if peer.Number != own.Number || !peer.StartedAt.Equal(own.StartedAt) || math.Abs(peer.DistanceMi-own.DistanceMi) > 0.01 ||
		math.Abs(peer.SoCUsedPct-own.SoCUsedPct) > 0.01 || math.Abs(peer.EnergyUsedKWh-own.EnergyUsedKWh) > 0.01 || peer.MaxSpeedMph != own.MaxSpeedMph {
		t.Errorf("peer %+v, owner %+v: the pods disagree", peer, own)
	}
}

func TestPeerPodWithNoOpenDrive(t *testing.T) {
	r := newDriveRig(t, 20)
	if d := r.peer.ActiveDrive("someone-elses"); d != nil {
		t.Errorf("unknown vehicle: %+v", d)
	}
	// A snapshot whose drive ended long ago is not an open drive.
	r.peer.now = func() time.Time { return r.at.Add(20*time.Minute + liveDriveMaxGap + time.Minute) }
	if d := r.peer.ActiveDrive(chargeTestVehicle); d != nil {
		t.Errorf("stale snapshot: %+v", d)
	}
	// A pod with no shared store has nothing to read.
	if d := NewStateMonitor(nil, nil).ActiveDrive(chargeTestVehicle); d != nil {
		t.Errorf("no store: %+v", d)
	}
	// A pod that streams the vehicle itself with no open drive of its own has none to report.
	r.peer.now = func() time.Time { return r.at.Add(20 * time.Minute) }
	r.peer.remoteMu.Lock()
	r.peer.peerDrives = nil
	r.peer.remoteMu.Unlock()
	r.peer.mu.Lock()
	r.peer.active[chargeTestVehicle] = func() {}
	r.peer.mu.Unlock()
	if d := r.peer.ActiveDrive(chargeTestVehicle); d != nil {
		t.Errorf("a streaming pod with no drive: %+v", d)
	}
}

// failingLoadStore is a live-state store whose snapshot read fails, counting the reads.
type failingLoadStore struct {
	*memLiveStateStore
	loads atomic.Int32
}

func (f *failingLoadStore) Load(context.Context, string) (LiveStateSnapshot, bool, error) {
	f.loads.Add(1)
	return LiveStateSnapshot{}, false, errors.New("store down")
}

// During a store outage a non-owner does not wait for the store on every poll: the failed read is remembered as "no drive" for the same short
// TTL as a good one, then the store is tried again.
func TestPeerDriveRemembersAStoreFailureBriefly(t *testing.T) {
	store := &failingLoadStore{memLiveStateStore: newMemLiveStateStore()}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	m.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if d := m.ActiveDrive(chargeTestVehicle); d != nil {
			t.Fatalf("a failing store gave a drive: %+v", d)
		}
	}
	if n := store.loads.Load(); n != 1 {
		t.Errorf("%d store reads for 5 polls inside the TTL, want 1", n)
	}
	now = now.Add(remoteLatestTTL + time.Second)
	if d := m.ActiveDrive(chargeTestVehicle); d != nil || store.loads.Load() != 2 {
		t.Errorf("after the TTL: drive %v, %d reads; want none and one more read", d, store.loads.Load())
	}
}
