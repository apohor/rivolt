package rivian

import (
	"context"
	"math"
	"testing"
	"time"
)

// R2 support from real frames (testdata/corpus/r2_*.json, anonymized prod
// data, played by the replay harness). An R2's vehicleState never reports park or sleep: the gear
// stays at the last D/R and power at ready. Parallax does report both.

// Park → drive → park → sleep, then resubscribe replays of the stale
// vehicleState gear and power: one drive, closed at the Parallax P,
// gear P and power sleep held through the replays, no drive reopened.
func TestR2ParkDriveParkSleep(t *testing.T) {
	r, events := replayCorpus(t, "r2_park_drive_park_sleep")
	opened, closedAfterPark, reopened := 0, false, false
	var wasOpen bool
	parkedAt := -1.0
	r.onStep = func(i int, e corpusEvent) {
		d, _ := r.session()
		if d != nil && !wasOpen {
			opened++
			if parkedAt >= 0 {
				reopened = true
			}
		}
		if e.Kind == "px" && e.RVM == rvmDriveGear && *e.Enum == 1 && *e.TS == 1500 && parkedAt < 0 {
			parkedAt = e.T
		}
		// Closed at the fresh P itself, before any resubscribe replay
		// (t=1665) could do it.
		if parkedAt >= 0 && d == nil && e.T < 1665 {
			closedAfterPark = true
		}
		// Mid-drive: a stop in traffic and a replayed D keep it open.
		if e.T > 74 && e.T < 1482 && d == nil {
			t.Errorf("t=%.0f: the drive closed mid-trip (%s %s)", e.T, e.Kind, e.RVM)
		}
		wasOpen = d != nil
	}
	r.run(t, events)

	if opened != 1 || reopened {
		t.Errorf("drives opened %d (reopened after park: %v), want exactly 1", opened, reopened)
	}
	if !closedAfterPark {
		t.Error("the drive didn't close at the fresh Parallax P (vehicleState kept R)")
	}
	c := r.cached()
	if c.Gear != "P" || c.PowerState != "sleep" {
		t.Errorf("after replays of R/ready: gear %q power %q, want P and sleep", c.Gear, c.PowerState)
	}
}

// Charging while vehicleState's gear still reads R (stationary, plugged,
// charging_active): a charge is recorded with energy, and the drive
// before it closes.
func TestR2ChargeWithStaleGear(t *testing.T) {
	r, events := replayCorpus(t, "r2_charge_with_stale_gear")
	sawDrive := false
	var charge *liveCharge
	var openedAt, closedAt float64 = -1, -1
	var peakEnergy float64
	r.onStep = func(i int, e corpusEvent) {
		d, c := r.session()
		if d != nil {
			sawDrive = true
		}
		if c != nil {
			if charge == nil {
				openedAt = e.T
			}
			charge = c
			if row := r.m.liveChargeRow(replayVehicle, c); row.EnergyAddedKWh > peakEnergy {
				peakEnergy = row.EnergyAddedKWh
			}
			if d != nil {
				t.Errorf("t=%.0f: drive and charge open together", e.T)
			}
		}
		if charge != nil && c == nil && closedAt < 0 {
			closedAt = e.T
		}
		if charge != nil && c != nil && c != charge {
			t.Errorf("t=%.0f: the charge split (a 0 kW pause must not end it)", e.T)
		}
	}
	r.run(t, events)

	if !sawDrive {
		t.Error("the drive before the charge never opened")
	}
	// No chargerStatus at all (an R2): charging_active at 0 kW doesn't
	// open it (t=179); power does (t=184); charging_complete closes it.
	if openedAt != 184 {
		t.Errorf("charge opened at t=%.0f, want 184 (first power while charging_active)", openedAt)
	}
	if closedAt != 900 {
		t.Errorf("charge closed at t=%.0f, want 900 (charging_complete)", closedAt)
	}
	if peakEnergy <= 0 || charge == nil || charge.maxPower < 7 {
		t.Errorf("charge: %.2f kWh, peak %.1f kW; want energy at ~7.4 kW", peakEnergy, charge.maxPower)
	}
}

// Without chargerStatus (R2): charging_connecting at 0 kW (29 of them in
// 5 days on 01-000000001, never followed by power) opens nothing; with a
// status (R1) the status decides, as before.
func TestPluggedWithoutChargerStatus(t *testing.T) {
	s := &liveSessions{}
	for _, c := range []struct {
		cs, status string
		kw         float64
		open, want bool
	}{
		{"charging_active", "", 7.4, false, true},
		{"charging_active", "", 0, false, false},
		{"charging_active", "", 0, true, true}, // a pause inside an open charge
		{"charging_connecting", "", 0, false, false},
		{"charging_connecting", "", 2, false, true},
		{"charging_ready", "", 0, true, false},
		{"charging_complete", "", 0, true, false},
		{"charging_active", "chrgr_sts_not_connected", 7.4, false, false}, // R1: status decides
		{"charging_ready", "chrgr_sts_connected_charging", 0, false, true},
	} {
		s.charge = nil
		if c.open {
			s.charge = &liveCharge{}
		}
		if got := s.plugged(&State{ChargerState: c.cs, ChargerStatus: c.status, ChargerPowerKW: c.kw}); got != c.want {
			t.Errorf("%s status=%q %.1f kW open=%v: plugged %v, want %v", c.cs, c.status, c.kw, c.open, got, c.want)
		}
	}
}

// An R2 reports charging_ready while driving and parked unplugged:
// charging_ready alone doesn't open a charge there, even when its
// charger status reads connected; charging_active does.
func TestR2ChargingReadyDoesNotOpenACharge(t *testing.T) {
	r := newReplayRun("R2")
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	push := func(sec int, cs string, kw float64) {
		r.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: at.Add(time.Duration(sec) * time.Second),
			Gear: "P", ChargerState: cs, ChargerStatus: "chrgr_sts_connected_charging",
			ChargerPowerKW: kw, BatteryLevelPct: 40, OdometerKm: 30000})
	}
	for s := 0; s < 600; s += 30 {
		push(s, "charging_ready", 0)
	}
	if _, c := r.session(); c != nil {
		t.Fatal("charging_ready opened a charge on an R2")
	}
	push(630, "charging_active", 7.2)
	if _, c := r.session(); c == nil {
		t.Fatal("charging_active didn't open a charge")
	}

	// An R1 (no evidence against its charging_ready) still opens on it.
	r1 := newReplayRun("R2")
	r1.m.vehicleInfo[replayVehicle] = &Vehicle{ID: replayVehicle, Model: "R1T"}
	r1.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: at, Gear: "P", ChargerState: "charging_ready",
		ChargerStatus: "chrgr_sts_connected_charging", BatteryLevelPct: 40, OdometerKm: 30000})
	if _, c := r1.session(); c == nil {
		t.Error("an R1's charging_ready no longer opens a charge")
	}

	// An R1 that reported charging_ready while driving (every R1 does)
	// still opens a charge on charging_ready once parked and plugged:
	// the rule is R2-only, not "seen ready while moving".
	r1m := newReplayRun("R2")
	r1m.m.vehicleInfo[replayVehicle] = &Vehicle{ID: replayVehicle, Model: "R1S"}
	r1m.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: at, Gear: "D", SpeedKph: 90,
		ChargerState: "charging_ready", ChargerStatus: "chrgr_sts_not_connected", BatteryLevelPct: 60, OdometerKm: 30000})
	r1m.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: at.Add(20 * time.Minute), Gear: "P",
		ChargerState: "charging_ready", ChargerStatus: "chrgr_sts_connected_charging", BatteryLevelPct: 55, OdometerKm: 30030})
	if _, c := r1m.session(); c == nil {
		t.Error("an R1 seen charging_ready while moving no longer opens a charge when plugged")
	}
}

// Parallax power enum 1 is sleep and 2 standby (fleet evidence); 5-7 are
// vehicle_reset, ota_update (confirmed live) and shutdown.
func TestPowerStateFromParallaxSleep(t *testing.T) {
	for v, want := range map[uint64]string{1: "sleep", 2: "standby", 3: "ready", 4: "go", 5: "vehicle_reset", 6: "ota_update", 7: "shutdown", 0: "", 8: "", 9: ""} {
		if got := powerStateFromParallax(v); got != want {
			t.Errorf("enum %d = %q, want %q", v, got, want)
		}
	}
}

// R2 pack: ~91.5 kWh (reported 91.47 / 91.64), not 75.
func TestInferPackKWhR2(t *testing.T) {
	if got := InferPackKWh("R2", "", 2027); got < 90 || got > 93 {
		t.Errorf("R2 pack %.1f kWh, want ~91.5", got)
	}
}

// R1 behaviour is unchanged: with a chargerStatus the status decides.
// Plugged and charging opens a charge, an R1's charging_ready while
// plugged still opens one at 0 kW, and the status turning not connected
// closes it even if charging_active and power linger (the sticky
// post-unplug frames the plug check exists for).
func TestR1ChargeFollowsChargerStatus(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)
	mk := func() *replayRun {
		r := newReplayRun("R2")
		r.m.vehicleInfo[replayVehicle] = &Vehicle{ID: replayVehicle, Model: "R1T"}
		return r
	}
	push := func(r *replayRun, sec int, cs, status string, kw float64) {
		r.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: at.Add(time.Duration(sec) * time.Second),
			Gear: "P", ChargerState: cs, ChargerStatus: status, ChargerPowerKW: kw, BatteryLevelPct: 50, OdometerKm: 30000})
	}

	r := mk()
	push(r, 0, "charging_active", "chrgr_sts_not_connected", 7.2)
	if _, c := r.session(); c != nil {
		t.Fatal("R1: charging_active with status not connected opened a charge")
	}
	push(r, 30, "charging_active", "chrgr_sts_connected_charging", 7.2)
	if _, c := r.session(); c == nil {
		t.Fatal("R1: plugged and charging didn't open a charge")
	}
	push(r, 60, "charging_active", "chrgr_sts_not_connected", 7.2)
	if _, c := r.session(); c != nil {
		t.Error("R1: status not connected didn't close the charge")
	}

	ready := mk()
	push(ready, 0, "charging_ready", "chrgr_sts_connected_charging", 0)
	if _, c := ready.session(); c == nil {
		t.Error("R1: plugged charging_ready no longer opens a charge")
	}
}

// An R2 can keep reporting charging_active after the cable is pulled
// (Rivian's charger fields stick), and its merged power sticks at the
// last value too, since a pushed 0 kW doesn't merge. Without a
// chargerStatus the open charge counts as plugged, so only the
// stale-session guard ends it: after liveChargeMaxGap with no SoC gain
// or state change, the charge closes (abandoned) at its last progress,
// and the same sticky frames don't open a new, empty one; a real change
// (a new charge with SoC rising, or a state change) still opens one. An R1 still reopens on a stale close
// while its status says connected, as before.
func TestR2StickyChargingActiveClosesOnce(t *testing.T) {
	r := newReplayRun("R2")
	ctx := context.Background()
	at := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	push := func(m *StateMonitor, min float64, cs, status string, kw, soc float64) {
		m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: at.Add(time.Duration(min * float64(time.Minute))),
			Gear: "P", ChargerState: cs, ChargerStatus: status, ChargerPowerKW: kw, BatteryLevelPct: soc, OdometerKm: 30000})
	}
	// 20 minutes at 7.4 kW, 50 % → 52.6 %.
	for i := 0; i <= 20; i++ {
		push(r.m, float64(i), "charging_active", "", 7.4, 50+float64(i)*0.13)
	}
	_, charge := r.session()
	if charge == nil {
		t.Fatal("the charge never opened")
	}
	first := charge.id
	lastReal := at.Add(20 * time.Minute)
	atLastReal := r.m.liveChargeRow(replayVehicle, charge)
	if atLastReal.EnergyAddedKWh <= 0 {
		t.Fatalf("no energy after 20 min at 7.4 kW: %+v", atLastReal)
	}
	// Unplugged: an hour of sticky charging_active (0 kW pushed, which
	// doesn't merge), flat SoC.
	opened := map[string]bool{first: true}
	for m := 23.0; m <= 80; m += 3 {
		push(r.m, m, "charging_active", "", 0, 52.6)
		if _, c := r.session(); c != nil {
			opened[c.id] = true
		}
	}
	if _, c := r.session(); c != nil {
		t.Errorf("a charge is still open after an hour of 0 kW sticky frames (%s)", c.id)
	}
	if len(opened) != 1 {
		t.Errorf("sticky frames opened %d more charge(s)", len(opened)-1)
	}
	if charge.finalState != "abandoned" || !charge.endAt.Equal(lastReal) {
		t.Errorf("closed %q ending %v, want abandoned at the last progress %v", charge.finalState, charge.endAt, lastReal)
	}
	// The sticky tail (its merged 7.4 kW kept integrating) adds nothing:
	// the row's energy and active time are those at the last progress.
	closed := r.m.liveChargeRow(replayVehicle, charge)
	if math.Abs(closed.EnergyAddedKWh-atLastReal.EnergyAddedKWh) > 1e-9 || closed.ActiveSeconds != atLastReal.ActiveSeconds {
		t.Errorf("closed with %.3f kWh / %.0f s active, want the %.3f kWh / %.0f s at the last progress",
			closed.EnergyAddedKWh, closed.ActiveSeconds, atLastReal.EnergyAddedKWh, atLastReal.ActiveSeconds)
	}
	// Driven while still stuck (SoC falls), then plugged in again: the
	// first SoC rise opens a new charge.
	push(r.m, 85, "charging_active", "", 0, 45)
	push(r.m, 88, "charging_active", "", 0, 44)
	if _, c := r.session(); c != nil {
		t.Errorf("a falling SoC in the stuck state opened %s", c.id)
	}
	push(r.m, 90, "charging_active", "", 7.4, 44.2)
	if _, c := r.session(); c == nil || c.id == first {
		t.Errorf("a real charge after the stuck state didn't open: %v", c)
	}

	// An R1 reports a connected status: a stale close reopens as before.
	r1 := newReplayRun("R2")
	r1.m.vehicleInfo[replayVehicle] = &Vehicle{ID: replayVehicle, Model: "R1S"}
	push(r1.m, 0, "charging_active", "chrgr_sts_connected_charging", 7.4, 50)
	_, c1 := r1.session()
	push(r1.m, 45, "charging_ready", "chrgr_sts_connected_charging", 0, 50)
	_, c2 := r1.session()
	if c1 == nil || c2 == nil || c1.id == c2.id {
		t.Errorf("R1: %v then %v, want a stale close and a new charge while still connected", c1, c2)
	}
}

// The stored and served gear follows Parallax park (qa2's R2 health
// check): an R2's vehicleState keeps the last R for hours while it stands
// and charges, or sends no gear at all. A Parallax P, fresh or a cloud
// replay of the last one, parks the cached gear, and vehicleState's stale R
// can't undo it while the truck stands still.
func TestR2GearFollowsParallaxPark(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	vs := func(r *replayRun, gear string, mph float64) {
		r.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: time.Now(), Gear: gear, SpeedKph: mph / kphToMi,
			ChargerState: "charging_active", ChargerPowerKW: 7.4, BatteryLevelPct: 60, OdometerKm: 30000})
	}
	px := func(r *replayRun, enum uint64, at time.Time) {
		r.m.onDriveDynamicsFrame(ctx, replayVehicle, DriveDynamicsFrame{RVM: rvmDriveGear, Value: enum, ValueOK: true, TimestampMs: at.UnixMilli()})
	}

	t.Run("stuck R, replayed P", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "R", 0.2)
		px(r, 1, now.Add(-17*time.Hour)) // the cloud's replay of the park, hours old
		if g := r.cached().Gear; g != "P" {
			t.Fatalf("after a replayed P: gear %q, want P", g)
		}
		vs(r, "R", 0.2) // vehicleState repeats the stale R
		if g := r.cached().Gear; g != "P" {
			t.Errorf("a stale vehicleState R undid the park: gear %q", g)
		}
	})

	t.Run("blank gear, fresh P", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "", 0)
		if g := r.cached().Gear; g != "" {
			t.Fatalf("setup: gear %q, want blank", g)
		}
		px(r, 1, time.Now())
		if g := r.cached().Gear; g != "P" {
			t.Errorf("blank gear after a fresh P: %q, want P", g)
		}
	})

	t.Run("blank gear, replayed P", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "", 0)
		px(r, 1, now.Add(-time.Hour))
		if g := r.cached().Gear; g != "P" {
			t.Errorf("blank gear after a replayed P: %q, want P", g)
		}
	})

	t.Run("a replayed P doesn't park a truck that is moving", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "", 30)
		px(r, 1, now.Add(-time.Hour))
		if g := r.cached().Gear; g == "P" {
			t.Error("a replayed P parked a truck doing 30 mph")
		}
	})
}
