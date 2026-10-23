package rivian

import (
	"context"
	"math"
	"testing"
	"time"
)

// A charge's energy survives a handover. When a vehicle's lease moves to another pod, the new pod rebuilds the charge from
// the Redis snapshot; the snapshot used to carry the active seconds but not the energy integral, so every handover mid-charge
// restarted the charge's energy at zero (an L2 charge came out with only the energy since the last handover).
const chargeTestVehicle = "veh-1"

func TestChargeEnergySurvivesAHandover(t *testing.T) {
	ctx := context.Background()
	store := newMemLiveStateStore()
	at := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	mk := func() *StateMonitor {
		m := NewStateMonitor(nil, nil)
		m.SetLiveStateStore(store)
		m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T"}
		return m
	}
	// 7.5 kW, a minute apart, SoC rising a tenth of a percent a minute.
	push := func(m *StateMonitor, minute int) {
		m.applyVehicleStatePush(ctx, chargeTestVehicle, &State{VehicleID: chargeTestVehicle, At: at.Add(time.Duration(minute) * time.Minute),
			Gear: "P", ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: 7.5,
			BatteryLevelPct: 20 + float64(minute)/10, OdometerKm: 30000})
	}
	energy := func(m *StateMonitor) float64 {
		m.sessMu.Lock()
		defer m.sessMu.Unlock()
		return m.liveChargeRow(chargeTestVehicle, m.sessions[chargeTestVehicle].charge).EnergyAddedKWh
	}

	owner := mk()
	for minute := 0; minute <= 240; minute++ {
		push(owner, minute)
	}
	want := 7.5 * 4 // four hours at 7.5 kW
	if got := energy(owner); math.Abs(got-want) > 0.2 {
		t.Fatalf("setup: the first pod has %.2f kWh, want about %.1f", got, want)
	}

	// The lease moves: a second pod picks up from the snapshot and keeps charging for another two hours.
	peer := mk()
	for minute := 241; minute <= 360; minute++ {
		push(peer, minute)
	}
	want = 7.5 * 6
	if got := energy(peer); math.Abs(got-want) > 0.3 {
		t.Errorf("after the handover the charge has %.2f kWh, want about %.1f (the first pod's four hours must not be lost)", got, want)
	}
}

func TestChargeSnapshotCarriesEnergyAndOlderSnapshotsStillLoad(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	s := &liveSessions{charge: &liveCharge{id: "c", startedAt: t0, endAt: t0.Add(time.Hour), energyIntKWh: 12.3, activeSeconds: 3600}}
	got := liveSessionsFromSnapshot(s.snapshot()).charge
	if got.energyIntKWh != 12.3 || got.progressKWh != 12.3 || got.activeSeconds != 3600 {
		t.Errorf("energy %.2f progress %.2f active %.0f after a round trip, want 12.3, 12.3, 3600", got.energyIntKWh, got.progressKWh, got.activeSeconds)
	}
	old := s.snapshot()
	old.Charge.EnergyIntKWh = 0 // a snapshot an older pod wrote
	if got := liveSessionsFromSnapshot(old).charge; got.energyIntKWh != 0 || got.activeSeconds != 3600 {
		t.Errorf("an older snapshot loaded as energy %.2f active %.0f", got.energyIntKWh, got.activeSeconds)
	}
}

func TestChargeEnergyFloor(t *testing.T) {
	const pack = 100.0
	for _, c := range []struct {
		name                     string
		energy, peak, start, end float64
		packKWh                  float64
		want                     float64
		floored                  bool
	}{
		{"a healthy AC charge is left alone", 36, 7.5, 20, 50, pack, 36, false},
		{"a little under the SoC energy is left alone", 20, 7.5, 20, 50, pack, 20, false},
		{"an integral that lost most of it is replaced, with the charging losses", 5, 7.5, 20, 50, pack, 32.4, true},
		{"just under the ratio is replaced", 17.9, 7.5, 20, 50, pack, 32.4, true},
		{"just over the ratio is left alone", 18.1, 7.5, 20, 50, pack, 18.1, false},
		{"no energy at all is replaced too", 0, 7.5, 20, 50, pack, 32.4, true},
		{"a DC session is left to its Parallax total", 5, 150, 20, 50, pack, 5, false},
		{"a small SoC step is not trusted", 0.1, 7.5, 20, 24, pack, 0.1, false},
		{"an unknown pack is left alone", 5, 7.5, 20, 50, 0, 5, false},
		{"no power seen is left alone", 5, 0, 20, 50, pack, 5, false},
		{"SoC falling is left alone", 5, 7.5, 50, 47, pack, 5, false},
		{"a start SoC of 0 (a failed seed) is not a 50 % gain", 5, 7.5, 0, 50, pack, 5, false},
		{"an end SoC over 100 is not trusted", 5, 7.5, 50, 140, pack, 5, false},
	} {
		got, floored := chargeEnergyFloor(c.energy, c.peak, c.start, c.end, c.packKWh)
		if math.Abs(got-c.want) > 1e-9 || floored != c.floored {
			t.Errorf("%s: got %.2f floored %v, want %.2f %v", c.name, got, floored, c.want, c.floored)
		}
	}
}

type floorCount struct{ n int }

func (f *floorCount) OnSleepEnded(string, time.Duration, bool) {}
func (f *floorCount) OnChargeEnergyFloor()                     { f.n++ }

// A floored charge is marked estimated, priced from the estimate, and counted once however often its row is rebuilt.
func TestFlooredChargeIsMarkedAndCountedOnce(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	obs := &floorCount{}
	m.sleepObserver = obs
	m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T", PackKWh: 100}
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	c := &liveCharge{id: "c", startedAt: t0, endAt: t0.Add(4 * time.Hour), startSoC: 20, endSoC: 50, maxPower: 7.5,
		energyIntKWh: 5, activeSeconds: 4 * 3600, finalState: "charging_active"}
	// While the charge is open its row shows the live energy: not floored, not marked, not counted.
	if row := m.liveChargeRow(chargeTestVehicle, c); row.EnergyEstimated || row.EnergyAddedKWh != 5 || obs.n != 0 {
		t.Fatalf("an open row: estimated %v, %.2f kWh, counted %d; want the live 5 kWh, unmarked", row.EnergyEstimated, row.EnergyAddedKWh, obs.n)
	}
	// Closed (charging_complete, or the final write of a rejected charge) it is floored, marked and counted once.
	c.finalState = "charging_complete"
	for i := 0; i < 3; i++ {
		row := m.liveChargeRow(chargeTestVehicle, c)
		if !row.EnergyEstimated || math.Abs(row.EnergyAddedKWh-32.4) > 1e-9 {
			t.Fatalf("row %d: estimated %v, %.2f kWh, want true and 32.40", i, row.EnergyEstimated, row.EnergyAddedKWh)
		}
	}
	if obs.n != 1 {
		t.Errorf("counted %d times, want once", obs.n)
	}
	// A healthy charge is not marked and not counted.
	ok := &liveCharge{id: "d", startedAt: t0, endAt: t0.Add(4 * time.Hour), startSoC: 20, endSoC: 50, maxPower: 7.5,
		energyIntKWh: 32, activeSeconds: 4 * 3600, finalState: "charging_complete"}
	if row := m.liveChargeRow(chargeTestVehicle, ok); row.EnergyEstimated || obs.n != 1 {
		t.Errorf("a healthy charge: estimated %v, counted %d", row.EnergyEstimated, obs.n)
	}
}
