package rivian

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// R2 support from real frames (testdata/r2_frames.json, anonymized prod
// data). An R2's vehicleState never reports park or sleep: the gear
// stays at the last D/R and power at ready. Parallax does report both.

type r2Event struct {
	T     float64  `json:"t"`
	Kind  string   `json:"kind"` // vs | px_gear | px_power
	Enum  uint64   `json:"enum"`
	TS    float64  `json:"ts"`
	Gear  string   `json:"gear"`
	Power string   `json:"power"`
	MPH   *float64 `json:"mph"`
	Odo   *float64 `json:"odo"`
	CS    string   `json:"cs"`
	CStat string   `json:"status"`
	KW    *float64 `json:"kw"`
	SoC   float64  `json:"soc"`
}

func loadR2(t *testing.T, name string) []r2Event {
	t.Helper()
	raw, err := os.ReadFile("testdata/r2_frames.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	var seq struct {
		Events []r2Event `json:"events"`
	}
	if err := json.Unmarshal(all[name], &seq); err != nil || len(seq.Events) == 0 {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return seq.Events
}

// r2Run feeds a fixture through the monitor's real vehicleState and
// Parallax drive-dynamics paths. at(e) is each vehicleState frame's
// time; a Parallax ts seen before is the cloud's replay (same event
// time), anything new is fresh. Speed is applied the way Parallax gnss
// does on an R2 (vehicleState's GPS is dropped there): 0 included.
type r2Run struct {
	m      *StateMonitor
	ts     map[float64]int64
	odoKm  float64
	onStep func(i int, e r2Event)
}

func newR2Run() *r2Run {
	m := NewStateMonitor(nil, nil)
	m.parallaxDriveDynamics = true
	m.vehicleInfo["r2"] = &Vehicle{ID: "r2", Model: "R2"}
	return &r2Run{m: m, ts: map[float64]int64{}, odoKm: 30000}
}

func (r *r2Run) run(t *testing.T, events []r2Event, at func(r2Event) time.Time) {
	t.Helper()
	ctx := context.Background()
	for i, e := range events {
		switch e.Kind {
		case "vs":
			if e.MPH != nil {
				r.m.mu.Lock()
				if c := r.m.cache["r2"]; c != nil {
					next := *c
					next.SpeedKph = *e.MPH / kphToMi
					r.m.cache["r2"] = &next
				}
				r.m.mu.Unlock()
			}
			st := &State{VehicleID: "r2", At: at(e), Gear: e.Gear, PowerState: e.Power,
				ChargerState: e.CS, ChargerStatus: e.CStat, BatteryLevelPct: e.SoC}
			if e.MPH != nil {
				st.SpeedKph = *e.MPH / kphToMi
			}
			if e.Odo != nil {
				st.OdometerKm = r.odoKm + *e.Odo/kmToMi
			} else if c := r.cached(); c == nil {
				st.OdometerKm = r.odoKm
			}
			if e.KW != nil {
				st.ChargerPowerKW = *e.KW
			}
			r.m.applyVehicleStatePush(ctx, "r2", st)
		case "px_gear", "px_power":
			ms, seen := r.ts[e.TS]
			if !seen {
				ms = time.Now().UnixMilli() + int64(len(r.ts))
				r.ts[e.TS] = ms
			}
			rvm := rvmDriveGear
			if e.Kind == "px_power" {
				rvm = rvmPowerState
			}
			r.m.onDriveDynamicsFrame(ctx, "r2", DriveDynamicsFrame{RVM: rvm, Value: e.Enum, ValueOK: true, TimestampMs: ms})
		default:
			t.Fatalf("event %d: kind %q", i, e.Kind)
		}
		if r.onStep != nil {
			r.onStep(i, e)
		}
	}
}

func (r *r2Run) cached() *State {
	r.m.mu.RLock()
	defer r.m.mu.RUnlock()
	return r.m.cache["r2"]
}

func (r *r2Run) session() (drive *liveDrive, charge *liveCharge) {
	r.m.sessMu.Lock()
	defer r.m.sessMu.Unlock()
	s := r.m.sessions["r2"]
	if s == nil {
		return nil, nil
	}
	return s.drive, s.charge
}

// Park → drive → park → sleep, then resubscribe replays of the stale
// vehicleState gear and power: one drive, closed at the Parallax P,
// gear P and power sleep held through the replays, no drive reopened.
func TestR2ParkDriveParkSleep(t *testing.T) {
	events := loadR2(t, "park_drive_park_sleep")
	r := newR2Run()
	opened, closedAfterPark, reopened := 0, false, false
	var wasOpen bool
	parkedAt := -1.0
	r.onStep = func(i int, e r2Event) {
		d, _ := r.session()
		if d != nil && !wasOpen {
			opened++
			if parkedAt >= 0 {
				reopened = true
			}
		}
		if e.Kind == "px_gear" && e.Enum == 1 && e.TS == 1500 && parkedAt < 0 {
			parkedAt = e.T
		}
		// Closed at the fresh P itself, before any resubscribe replay
		// (t=1665) could do it.
		if parkedAt >= 0 && d == nil && e.T < 1665 {
			closedAfterPark = true
		}
		// Mid-drive: a stop in traffic and a replayed D keep it open.
		if e.T > 74 && e.T < 1482 && d == nil {
			t.Errorf("t=%.0f: the drive closed mid-trip (%s)", e.T, e.Kind)
		}
		wasOpen = d != nil
	}
	r.run(t, events, func(r2Event) time.Time { return time.Now() })

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
	events := loadR2(t, "charge_with_stale_gear")
	r := newR2Run()
	base := time.Date(2026, 10, 13, 3, 40, 53, 0, time.UTC)
	sawDrive := false
	r.onStep = func(i int, e r2Event) {
		if d, _ := r.session(); d != nil {
			sawDrive = true
		}
	}
	r.run(t, events, func(e r2Event) time.Time { return base.Add(time.Duration(e.T * float64(time.Second))) })

	d, c := r.session()
	if !sawDrive {
		t.Error("the drive before the charge never opened")
	}
	if d != nil {
		t.Error("the drive is still open while the car charges")
	}
	if c == nil {
		t.Fatal("no charge opened while charging_active with a stale R")
	}
	row := r.m.liveChargeRow("r2", c)
	if row.EnergyAddedKWh <= 0 || row.MaxPowerKW < 7 {
		t.Errorf("charge: %.2f kWh at %.1f kW peak, want energy at ~7.4 kW", row.EnergyAddedKWh, row.MaxPowerKW)
	}
}

// An R2 reports charging_ready while driving and parked unplugged:
// charging_ready alone doesn't open a charge there, even when its
// charger status reads connected; charging_active does.
func TestR2ChargingReadyDoesNotOpenACharge(t *testing.T) {
	r := newR2Run()
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	push := func(sec int, cs string, kw float64) {
		r.m.applyVehicleStatePush(ctx, "r2", &State{VehicleID: "r2", At: at.Add(time.Duration(sec) * time.Second),
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
	r1 := newR2Run()
	r1.m.vehicleInfo["r2"] = &Vehicle{ID: "r2", Model: "R1T"}
	r1.m.applyVehicleStatePush(ctx, "r2", &State{VehicleID: "r2", At: at, Gear: "P", ChargerState: "charging_ready",
		ChargerStatus: "chrgr_sts_connected_charging", BatteryLevelPct: 40, OdometerKm: 30000})
	if _, c := r1.session(); c == nil {
		t.Error("an R1's charging_ready no longer opens a charge")
	}

	// An R1 that reported charging_ready while driving (every R1 does)
	// still opens a charge on charging_ready once parked and plugged:
	// the rule is R2-only, not "seen ready while moving".
	r1m := newR2Run()
	r1m.m.vehicleInfo["r2"] = &Vehicle{ID: "r2", Model: "R1S"}
	r1m.m.applyVehicleStatePush(ctx, "r2", &State{VehicleID: "r2", At: at, Gear: "D", SpeedKph: 90,
		ChargerState: "charging_ready", ChargerStatus: "chrgr_sts_not_connected", BatteryLevelPct: 60, OdometerKm: 30000})
	r1m.m.applyVehicleStatePush(ctx, "r2", &State{VehicleID: "r2", At: at.Add(20 * time.Minute), Gear: "P",
		ChargerState: "charging_ready", ChargerStatus: "chrgr_sts_connected_charging", BatteryLevelPct: 55, OdometerKm: 30030})
	if _, c := r1m.session(); c == nil {
		t.Error("an R1 seen charging_ready while moving no longer opens a charge when plugged")
	}
}

// Parallax power enum 1 is sleep and 2 standby (fleet evidence).
func TestPowerStateFromParallaxSleep(t *testing.T) {
	for v, want := range map[uint64]string{1: "sleep", 2: "standby", 3: "ready", 4: "go", 0: "", 9: ""} {
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
