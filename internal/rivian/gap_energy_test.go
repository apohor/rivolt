package rivian

import (
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

// A dropout longer than the integration cap used to cost the charge all the energy past the cap. When the SoC rose across
// the dropout, the missing energy is credited, kept apart (gapKWh), and bounded by what the SoC and the power allow.
func TestAccumulateCreditsAGapFromTheSoC(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	const pack = 131.0
	run := func(socAfter, packKWh float64) *liveCharge {
		c := &liveCharge{}
		for i := 0; i <= 10; i++ { // ten minutes at 7.5 kW, SoC flat at 20
			c.accumulate(t0.Add(time.Duration(i)*time.Minute), 7.5, 20, packKWh)
		}
		// 40 minutes with no frames, then 7.5 kW again.
		c.accumulate(t0.Add(50*time.Minute), 7.5, socAfter, packKWh)
		return c
	}
	near := func(got, want float64) bool { return math.Abs(got-want) < 0.01 }

	// Bounded by the power: 40 min at 7.5 kW is 5 kWh, the 2 min cap already counted 0.25.
	c := run(24, pack) // 4% of 131 kWh x 1.08 = 5.66 kWh, more than the power allows
	if !near(c.gapKWh, 4.75) || !near(c.energyIntKWh, 1.25+4.75+0.25) {
		t.Errorf("power-bounded gap: gap %.2f, total %.2f kWh, want 4.75 and 6.25", c.gapKWh, c.energyIntKWh)
	}
	// Bounded by the SoC: a 1% rise is 1.41 kWh at the wall, 0.25 of it counted.
	c = run(21, pack)
	if !near(c.gapKWh, 1.31*1.08-0.25) {
		t.Errorf("soc-bounded gap credited %.2f kWh, want %.2f", c.gapKWh, 1.31*1.08-0.25)
	}
	// No SoC rise, or no pack size: the clamp stands and nothing is credited, but the cut seconds are counted.
	for name, c := range map[string]*liveCharge{"flat soc": run(20, pack), "no pack": run(24, 0)} {
		if c.gapKWh != 0 || !near(c.energyIntKWh, 1.5) {
			t.Errorf("%s: gap %.2f, total %.2f kWh, want 0 and 1.5", name, c.gapKWh, c.energyIntKWh)
		}
		if int(c.clampedSeconds) != 38*60 {
			t.Errorf("%s: %v s clamped, want %d", name, c.clampedSeconds, 38*60)
		}
	}
	// An ordinary cadence is no gap.
	c = &liveCharge{}
	for i := 0; i <= 30; i++ {
		c.accumulate(t0.Add(time.Duration(i)*time.Minute), 7.5, 20+float64(i)/10, pack)
	}
	if c.gapKWh != 0 || c.clampedSeconds != 0 {
		t.Errorf("a frame a minute credited %.2f kWh and clamped %v s", c.gapKWh, c.clampedSeconds)
	}
}

// A charge picked up from its stored row (a restart, a handover) treats the time since the row's last frame as a gap, and the
// credit survives into the next row and the next resume.
func TestResumedChargeCreditsTheStretchSinceItsRow(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	row := &charges.Charge{
		ID: "live_veh-1_c_1", Source: "live", FinalState: "charging_active",
		StartedAt: t0, EndedAt: t0.Add(time.Hour),
		StartSoCPct: 20, EndSoCPct: 30, EnergyAddedKWh: 13, ActiveSeconds: 3600, AvgPowerKW: 7.5, MaxPowerKW: 7.5,
	}
	c := liveChargeFromRow(row)
	c.accumulate(t0.Add(time.Hour+30*time.Minute), 7.5, 33, 131) // half an hour later, SoC 30 -> 33
	want := 3.75 - 0.25                                          // the power's 30 min, less the capped 2 min already counted
	if math.Abs(c.gapKWh-want) > 0.01 || c.resumes != 1 {
		t.Fatalf("gap %.2f kWh after %d resumes, want %.2f after 1", c.gapKWh, c.resumes, want)
	}
	again := charges.Charge{EnergyAddedKWh: c.energyIntKWh, GapKWh: c.gapKWh}
	if got := liveChargeFromRow(&again); got.gapKWh != c.gapKWh {
		t.Errorf("gap %.2f after another resume, want %.2f", got.gapKWh, c.gapKWh)
	}
}

// Energy that is materially (10 % or more) credited across gaps is an estimate and is marked like a floored one, with gap_kwh
// saying how much; a small credit leaves the mark off but still shows. Gap credit never feeds the floor counter.
func TestGapCreditedChargeIsMarkedEstimated(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	obs := &floorCount{}
	m.sleepObserver = obs
	m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T", PackKWh: 100}
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	mk := func(energy, gap float64) *liveCharge {
		return &liveCharge{id: "c", startedAt: t0, endAt: t0.Add(4 * time.Hour), startSoC: 20, endSoC: 50, maxPower: 7.5,
			energyIntKWh: energy, gapKWh: gap, activeSeconds: 4 * 3600, finalState: "charging_complete"}
	}
	row := m.liveChargeRow(chargeTestVehicle, mk(30, 12))
	if !row.EnergyEstimated || math.Abs(row.GapKWh-12) > 1e-9 || row.EnergyAddedKWh != 30 {
		t.Errorf("40%% of the energy credited: estimated %v, gap %.2f, energy %.2f; want marked, 12, 30", row.EnergyEstimated, row.GapKWh, row.EnergyAddedKWh)
	}
	row = m.liveChargeRow(chargeTestVehicle, mk(30, 1))
	if row.EnergyEstimated || math.Abs(row.GapKWh-1) > 1e-9 {
		t.Errorf("a small credit: estimated %v, gap %.2f; want unmarked and 1", row.EnergyEstimated, row.GapKWh)
	}
	if obs.n != 0 {
		t.Errorf("gap credit counted %d times as the energy floor, want 0", obs.n)
	}
}
