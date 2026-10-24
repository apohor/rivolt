package rivian

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"
)

// On a pod that does not hold the vehicle's lease, LiveSessionForCharge reads the owner's published accumulator, so the
// live-session endpoint answers the same on every pod (the peer used to return the zeroed payload with no estimated cost).

type floorCounter struct {
	SleepObserver
	n atomic.Int32
}

func (f *floorCounter) OnChargeEnergyFloor() { f.n.Add(1) }

type peerRig struct {
	owner, peer *StateMonitor
	store       *memLiveStateStore
	at          time.Time
}

// newPeerRig has an owner pod that has recorded `minutes` of a 7.5 kW charge and a peer that shares its store.
func newPeerRig(t *testing.T, minutes int) *peerRig {
	t.Helper()
	ctx := context.Background()
	r := &peerRig{store: newMemLiveStateStore(), at: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	mk := func() *StateMonitor {
		m := NewStateMonitor(nil, nil)
		m.SetLiveStateStore(r.store)
		m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T"}
		m.now = func() time.Time { return r.at.Add(time.Duration(minutes) * time.Minute) }
		return m
	}
	r.owner, r.peer = mk(), mk()
	for minute := 0; minute <= minutes; minute++ {
		r.owner.applyVehicleStatePush(ctx, chargeTestVehicle, &State{VehicleID: chargeTestVehicle, At: r.at.Add(time.Duration(minute) * time.Minute),
			Gear: "P", ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: 7.5,
			BatteryLevelPct: 20 + float64(minute)/10, OdometerKm: 30000})
	}
	return r
}

func TestPeerPodSeesTheOwnersOpenCharge(t *testing.T) {
	r := newPeerRig(t, 120)
	sess, own := r.owner.LiveSessionForCharge(chargeTestVehicle)
	if own.EnergyKWh < 14 || own.EnergyKWh > 16 || own.PeakKW != 7.5 {
		t.Fatalf("setup: the owner has %+v (sess %v), want about 15 kWh at 7.5 kW", own, sess)
	}
	psess, peer := r.peer.LiveSessionForCharge(chargeTestVehicle)
	if psess != nil {
		t.Errorf("the peer has no Rivian session to serve, got %+v", psess)
	}
	if peer.EnergyKWh != own.EnergyKWh || peer.PeakKW != own.PeakKW || peer.Place != own.Place {
		t.Errorf("peer %+v, owner %+v: the pods disagree", peer, own)
	}
	if !peer.HomeRateApplies() {
		t.Error("an AC charge with no known place should take the home rate on the peer too")
	}
}

func TestPeerPodWithNothingToSee(t *testing.T) {
	r := newPeerRig(t, 30)
	// No snapshot for another vehicle.
	if _, n := r.peer.LiveSessionForCharge("someone-elses"); n != (ChargeNow{}) {
		t.Errorf("unknown vehicle: %+v", n)
	}
	// A snapshot whose charge ended long ago is not an open charge.
	r.peer.now = func() time.Time { return r.at.Add(30*time.Minute + liveChargeMaxGap + time.Minute) }
	if _, n := r.peer.LiveSessionForCharge(chargeTestVehicle); n != (ChargeNow{}) {
		t.Errorf("stale snapshot: %+v", n)
	}
	// A pod with no shared store has nothing to read.
	bare := NewStateMonitor(nil, nil)
	if _, n := bare.LiveSessionForCharge(chargeTestVehicle); n != (ChargeNow{}) {
		t.Errorf("no store: %+v", n)
	}
	// A pod that streams the vehicle itself, with no open charge of its own, has none to report.
	r.peer.now = func() time.Time { return r.at.Add(30 * time.Minute) }
	r.peer.remoteMu.Lock()
	r.peer.peerCharge = nil
	r.peer.remoteMu.Unlock()
	r.peer.mu.Lock()
	r.peer.active[chargeTestVehicle] = func() {}
	r.peer.mu.Unlock()
	if _, n := r.peer.LiveSessionForCharge(chargeTestVehicle); n != (ChargeNow{}) {
		t.Errorf("a streaming pod with no charge: %+v", n)
	}
}

// A pod that used to hold the lease may still have the previous plug-in's session cached, bonded to this very charge or to
// another: the peer must use the snapshot's own integral, never that session (the #1026 bug).
func TestPeerPodIgnoresItsOwnCachedSession(t *testing.T) {
	r := newPeerRig(t, 120)
	_, own := r.owner.LiveSessionForCharge(chargeTestVehicle)
	r.owner.sessMu.Lock()
	chargeID := r.owner.sessions[chargeTestVehicle].charge.id
	r.owner.sessMu.Unlock()
	r.peer.mu.Lock()
	r.peer.lastSession[chargeTestVehicle] = &LiveSession{VehicleID: chargeTestVehicle, Active: true, PowerKW: 7.5, TotalChargedEnergyKWh: 16.5}
	r.peer.lastSessionFor[chargeTestVehicle] = chargeID
	r.peer.mu.Unlock()
	sess, peer := r.peer.LiveSessionForCharge(chargeTestVehicle)
	if sess != nil {
		t.Errorf("the peer served its own cached session: %+v", sess)
	}
	if math.Abs(peer.EnergyKWh-own.EnergyKWh) > 0.01 {
		t.Errorf("peer energy %.2f kWh, owner %.2f: the cached session's 16.5 kWh must not count", peer.EnergyKWh, own.EnergyKWh)
	}
}

// The peer path is read-only: a snapshot that reads as closing and floored must not tick the energy-floor counter on every
// read (the once-per-charge guard lives on the instance a snapshot read rebuilds each time).
func TestPeerPodReadsDoNotCountTheEnergyFloor(t *testing.T) {
	r := newPeerRig(t, 120)
	snap, ok, _ := r.store.Load(context.Background(), chargeTestVehicle)
	if !ok || snap.Charge == nil {
		t.Fatal("no snapshot")
	}
	// Make the charge look closed with an integral that lost most of its energy: the floor replaces it.
	snap.Charge.FinalState = "unplugged" // not an open state, so this reads as a closing build
	snap.Charge.EnergyIntKWh = 1
	snap.Charge.StartSoC, snap.Charge.EndSoC = 20, 60
	snap.Charge.StartedAt = snap.Charge.EndAt.Add(-8 * time.Hour) // long enough that the power-times-time cap does not undo the floor
	_ = r.store.Save(context.Background(), chargeTestVehicle, snap, time.Hour)
	obs := &floorCounter{}
	r.peer.SetSleepObserver(obs)
	for i := 0; i < 10; i++ {
		r.peer.remoteMu.Lock()
		r.peer.peerCharge = nil // bypass the read cache, as a viewer polling every few seconds would
		r.peer.remoteMu.Unlock()
		if _, n := r.peer.LiveSessionForCharge(chargeTestVehicle); n.EnergyKWh <= 1 {
			t.Fatalf("read %d: energy %.2f; the floor should have raised it", i, n.EnergyKWh)
		}
	}
	if got := obs.n.Load(); got != 0 {
		t.Errorf("the energy-floor counter ticked %d times across 10 peer reads, want 0", got)
	}
}

func TestPeerPodCachesTheSnapshotRead(t *testing.T) {
	r := newPeerRig(t, 60)
	_, a := r.peer.LiveSessionForCharge(chargeTestVehicle)
	_ = r.store.Delete(context.Background(), chargeTestVehicle)
	_, b := r.peer.LiveSessionForCharge(chargeTestVehicle)
	if a != b || a.EnergyKWh == 0 {
		t.Errorf("within the cache window the second read should repeat the first: %+v then %+v", a, b)
	}
}
