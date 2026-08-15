package recap

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// mkSamples builds a sample series from (offsetSeconds, speedMph)
// pairs. Offsets are float seconds so tests can express sub-second
// spacing, which is the whole point of two of them.
func mkSamples(base time.Time, pts [][2]float64) []samples.Sample {
	out := make([]samples.Sample, 0, len(pts))
	for _, p := range pts {
		out = append(out, samples.Sample{
			At:       base.Add(time.Duration(p[0] * float64(time.Second))),
			SpeedMph: p[1],
		})
	}
	return out
}

// A steady 5 s cadence cruise with one deceleration to a stop. Regen
// should be a small, positive, physically plausible fraction of draw.
func TestAnalyzeDrivePowerPlausibleOnCleanSamples(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	pts := [][2]float64{}
	// 3 minutes at 55 mph, then decelerate to 0 over 20 s.
	for i := 0; i < 36; i++ {
		pts = append(pts, [2]float64{float64(i) * 5, 55})
	}
	for i := 1; i <= 4; i++ {
		pts = append(pts, [2]float64{180 + float64(i)*5, 55 - float64(i)*13.75})
	}
	pa := AnalyzeDrivePower(mkSamples(base, pts), drives.Drive{EnergyUsedKWh: 1.2})

	if pa.DrawKwh <= 0 {
		t.Fatalf("DrawKwh = %v, want > 0", pa.DrawKwh)
	}
	// The invariant that actually matters: you cannot recover more
	// than you spent.
	if pa.RegenKwh > pa.DrawKwh {
		t.Errorf("RegenKwh %v > DrawKwh %v — regen cannot exceed draw",
			pa.RegenKwh, pa.DrawKwh)
	}
	if pa.RegenPct < 0 || pa.RegenPct > 100 {
		t.Errorf("RegenPct = %v, want within [0,100]", pa.RegenPct)
	}
}

// Regression: near-duplicate rows must not blow up dv/dt.
//
// The live-merge path writes near-duplicate rows when a WS frame and
// a REST fallback land together, with speeds differing by up to
// 63 mph. 5,423 recorded intervals sit between 1 ms and 0.5 s.
// fAccel = mass * dv/dt turned those into ~1e6 kW spikes which, being
// emitted at interval midpoints, were then integrated across the
// seconds separating them from their neighbours — landing in
// RegenKwh. The calibration clamp of [0.5, 2.0] cannot absorb that.
func TestAnalyzeDrivePowerRejectsNearDuplicateSamples(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	pts := [][2]float64{}
	for i := 0; i < 20; i++ {
		pts = append(pts, [2]float64{float64(i) * 5, 55})
	}
	clean := AnalyzeDrivePower(mkSamples(base, pts), drives.Drive{EnergyUsedKWh: 0.8})

	// 1 ms after the 50 s row, not 1 µs: derivePower works in
	// UnixMilli, so anything finer truncates to dt = 0 and is
	// rejected as non-monotonic — testing nothing. 1 ms is the
	// tightest interval this model can actually see, and it is
	// plenty: a 55 mph drop over 1 ms is ~24,600 m/s^2.
	//
	// Inserted AFTER the 50 s sample so the series stays monotonic.
	poisoned := append([][2]float64{}, pts[:11]...)
	poisoned = append(poisoned,
		[2]float64{50.001, 0},
	)
	poisoned = append(poisoned, pts[11:]...)
	got := AnalyzeDrivePower(mkSamples(base, poisoned), drives.Drive{EnergyUsedKWh: 0.8})

	if got.RegenKwh > got.DrawKwh {
		t.Errorf("RegenKwh %v > DrawKwh %v — the 1 ms pair was integrated",
			got.RegenKwh, got.DrawKwh)
	}
	// Before the fix this was larger by many orders of magnitude.
	if got.RegenKwh > clean.RegenKwh+0.5 {
		t.Errorf("RegenKwh = %v with a 1 ms neighbour, %v without; "+
			"the sub-second interval must be rejected",
			got.RegenKwh, clean.RegenKwh)
	}
}

// Regression: a telemetry hole must not be integrated across.
//
// derivePower skips intervals outside [minSampleDtSec, maxSampleDtSec]
// and emits no point for them, but the integrators used to compute dt
// between surviving points — spanning the hole. 968 of 1860 recorded
// drives contain >30 s gaps and one is 95.9 % gap by time, so a
// boundary sample sitting in regen was multiplied across minutes.
func TestAnalyzeDrivePowerDoesNotIntegrateAcrossGaps(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	contiguous := [][2]float64{}
	for i := 0; i < 12; i++ {
		contiguous = append(contiguous, [2]float64{float64(i) * 5, 45})
	}
	// Same series, but with a 20-minute hole in the middle. The
	// modelled power either side is identical, so any energy the
	// gapped version reports beyond the contiguous one is phantom.
	gapped := append([][2]float64{}, contiguous[:6]...)
	for i := 6; i < 12; i++ {
		gapped = append(gapped, [2]float64{1200 + float64(i)*5, 45})
	}

	a := AnalyzeDrivePower(mkSamples(base, contiguous), drives.Drive{EnergyUsedKWh: 0.4})
	b := AnalyzeDrivePower(mkSamples(base, gapped), drives.Drive{EnergyUsedKWh: 0.4})

	if b.RegenKwh > a.RegenKwh+0.05 {
		t.Errorf("gapped RegenKwh = %v, contiguous = %v — the hole was integrated",
			b.RegenKwh, a.RegenKwh)
	}
	if b.DrawKwh > a.DrawKwh*1.5 {
		t.Errorf("gapped DrawKwh = %v, contiguous = %v — the hole was integrated",
			b.DrawKwh, a.DrawKwh)
	}
}

// The Gap flag is the contract every integrator depends on, so assert
// it directly rather than only through the totals.
func TestDerivePowerMarksDiscontinuities(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ss := mkSamples(base, [][2]float64{
		{0, 40}, {5, 42}, {10, 41},
		{600, 44}, {605, 43}, {610, 45}, // after a 10-minute hole
	})
	out := derivePower(ss, 0) // no calibration, keeps this focused

	if len(out) < 4 {
		t.Fatalf("derivePower returned %d points, want >= 4", len(out))
	}
	if !out[0].Gap {
		t.Error("first point must be marked Gap: it has no predecessor to integrate against")
	}
	gaps := 0
	for _, p := range out[1:] {
		if p.Gap {
			gaps++
		}
	}
	if gaps != 1 {
		t.Errorf("got %d interior gap markers, want exactly 1 (the 10-minute hole)", gaps)
	}
}

// Sub-second and over-long intervals must both be dropped, and each
// must leave a marker behind.
func TestDerivePowerRejectsOutOfBoundsIntervals(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ss := mkSamples(base, [][2]float64{
		{0, 30},
		{0.002, 60}, // 2 ms — survives UnixMilli, far below minSampleDtSec
		{5, 30},
		{10, 31},
		{500, 33}, // far above maxSampleDtSec
		{505, 32},
	})
	out := derivePower(ss, 0)
	for i, p := range out {
		if p.Y > 1e4 || p.Y < -1e4 {
			t.Fatalf("point %d has |Y| = %v kW — an out-of-bounds interval survived", i, p.Y)
		}
	}
}
