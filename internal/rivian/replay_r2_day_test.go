package rivian

import (
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
)

// An R2-shaped day, replayed (testdata/corpus/r2_day_drive_charge_drive.json, synthetic: derived in shape from one prod day
// and perturbed, so none of its values is a real user's; Sprint 8 item 1): asleep, woken, a drive, a plug-in and a
// multi-hour charge, hours asleep still plugged in, woken, a second drive, parked and asleep. An R2's vehicleState never says park or sleep and its charger fields
// stick, so this is the day the harness exists for: Parallax closes the drives, and the sticky tail, the wake-ups and the
// cloud's replays open nothing and split nothing.
//
// The expected figures are the corpus's own (offsets in seconds from its first event): drive 1 from 452 to 1834, 9.80 mi,
// SoC 52.0 to 48.2; the charge from 1938 to 17663, 7.5 kW at most, SoC 48.2 to 82.3; drive 2 from 30213 to 32033, 17.02 mi,
// SoC 81.9 to 76.6. A drive ends at the last moving sample, and the harness reads it to within the few seconds a frame
// takes to arrive, so the checks allow 20 s; energy comes from integrating the corpus's 1 %-step SoC and kW samples.
func TestCorpusR2DriveChargeDrive(t *testing.T) {
	r, events := replayCorpus(t, "r2_day_drive_charge_drive")
	got := r.trackSessions()
	var driveRows []drives.Drive
	var chargeRows []charges.Charge
	var drive *liveDrive
	var charge *liveCharge
	prev := r.onStep
	r.onStep = func(i int, e corpusEvent) {
		prev(i, e)
		d, c := r.session()
		// The row of a session as it closed: read from the session's own record once the monitor has let it go.
		if d != drive {
			if drive != nil {
				driveRows = append(driveRows, r.m.liveDriveRow(replayVehicle, drive))
			}
			drive = d
		}
		if c != charge {
			if charge != nil {
				chargeRows = append(chargeRows, r.m.liveChargeRow(replayVehicle, charge))
			}
			charge = c
		}
	}
	r.run(t, events)

	near := func(what string, got, want, tol float64) {
		t.Helper()
		if math.Abs(got-want) > tol {
			t.Errorf("%s = %.2f, want %.2f ± %.2f", what, got, want, tol)
		}
	}
	off := func(at time.Time) float64 { return at.Sub(corpusEpoch).Seconds() }

	if len(got.Drives) != 2 || len(got.Charges) != 1 {
		t.Fatalf("sessions: drives %v charges %v, want two drives and one charge", got.Drives, got.Charges)
	}
	if len(driveRows) != 2 || len(chargeRows) != 1 {
		t.Fatalf("closed rows: %d drives, %d charges", len(driveRows), len(chargeRows))
	}

	for i, w := range []struct{ start, end, miles, soc0, soc1 float64 }{
		{452, 1834, 9.80, 52.0, 48.2},
		{30213, 32033, 17.02, 81.9, 76.6},
	} {
		d := driveRows[i]
		near("drive start", off(d.StartedAt), w.start, 20)
		near("drive end", off(d.EndedAt), w.end, 20)
		near("drive miles", d.DistanceMi, w.miles, 0.15)
		near("drive start SoC", d.StartSoCPct, w.soc0, 0.3)
		near("drive end SoC", d.EndSoCPct, w.soc1, 0.3)
		// The drive is closed at the Parallax P, not left open by the stale R, and not before the stop.
		near("drive close offset", got.Drives[i][1], w.end+10, 30)
		if d.EnergyUsedKWh <= 0 {
			t.Errorf("drive %d used no energy", i+1)
		}
	}

	c := chargeRows[0]
	near("charge start", off(c.StartedAt), 1938, 20)
	near("charge end", off(c.EndedAt), 17663, 20)
	// The corpus samples a charge in 1 % steps, minutes apart: the integral alone (about 9 kWh) is most of an hour of the
	// 4.4 hours, and the rest is credited from the SoC gained across the gaps (kept apart as gap_kwh), at most what 7.5 kW
	// could deliver in them.
	near("charge kWh", c.EnergyAddedKWh, 32.7, 1.0)
	if c.GapKWh < 20 || c.GapKWh > c.EnergyAddedKWh || c.EnergyEstimated {
		t.Errorf("gap %.2f kWh of %.2f (estimated %v), want most of the energy credited as gaps and not the floor", c.GapKWh, c.EnergyAddedKWh, c.EnergyEstimated)
	}
	near("charge peak kW", c.MaxPowerKW, 7.5, 0.1)
	near("charge start SoC", c.StartSoCPct, 48.2, 0.3)
	near("charge end SoC", c.EndSoCPct, 82.3, 0.3)
	// Closed by charging_complete (17663), not by the sleep, the later wake-up or a replay.
	near("charge close offset", got.Charges[0][1], 17663, 5)

	// Parked and asleep at the end, though vehicleState's last gear was R and its power go.
	if s := r.cached(); s.Gear != "P" || s.PowerState != "sleep" {
		t.Errorf("at the end: gear %q power %q, want P and sleep", s.Gear, s.PowerState)
	}
}

// The same day's power state, as the cache reads it after the event at each offset. An R2's vehicleState says only
// ready or go, and sticks there; Parallax gives sleep, standby, ready and go, and the cloud's replays of an old
// value (the same frame, arriving again) must not undo a newer one.
func TestCorpusR2PowerSequence(t *testing.T) {
	r, events := replayCorpus(t, "r2_day_drive_charge_drive")
	type step struct{ gear, power string }
	after := map[float64]step{}
	r.onStep = func(i int, e corpusEvent) {
		c := r.cached()
		after[e.T] = step{c.Gear, c.PowerState} // the last event at an offset wins
	}
	r.run(t, events)
	for _, w := range []struct {
		t           float64
		gear, power string
		why         string
	}{
		{0, "", "", "nothing from Parallax yet, and vehicleState gave no power"},
		{420, "", "standby", "woken: standby"},
		{425, "P", "ready", "ready, parked"},
		{447, "P", "go", "go"},
		{452, "D", "go", "in gear"},
		{533, "D", "go", "a replay of go mid-drive changes nothing"},
		{1839, "P", "go", "parked again before ready"},
		{1872, "P", "ready", "ready, plugged in"},
		{2147, "P", "ready", "a replay of ready during the charge"},
		{11645, "P", "ready", "a replay of ready, hours in"},
		{17666, "P", "standby", "the charge is done: standby"},
		{17677, "P", "sleep", "asleep, still plugged in"},
		{17769, "P", "sleep", "a replay of sleep"},
		{28777, "P", "standby", "woken at 10:05"},
		{28782, "P", "ready", ""},
		{29320, "P", "sleep", "asleep again"},
		{30114, "P", "standby", "woken for the drive"},
		{30119, "P", "ready", ""},
		{30212, "P", "go", ""},
		{30303, "D", "go", "a replay of go mid-drive"},
		{32050, "P", "go", "parked"},
		{32078, "P", "ready", ""},
		{32173, "P", "standby", ""},
		{32185, "P", "sleep", "asleep at the end"},
	} {
		s, ok := after[w.t]
		if !ok {
			t.Errorf("t=%v: no event at that offset (the corpus changed?)", w.t)
			continue
		}
		if s.power != w.power || (w.gear != "" && s.gear != w.gear) {
			t.Errorf("t=%v (%s): gear %q power %q, want %q and %q", w.t, w.why, s.gear, s.power, w.gear, w.power)
		}
	}
}
