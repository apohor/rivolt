package recap

import (
	"math"
	"sort"

	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// PowerAnalysis is a Go port of analyzeDrivePower from
// web/src/lib/power.ts. It estimates instantaneous battery-side power
// across a drive from physics (speed + acceleration + grade + drag)
// and integrates it into total draw and regen totals. The two
// implementations need to stay in lock-step: the SPA uses the TS copy
// for the chart's red/green ribbon and the "Regen recovered" tile;
// this copy feeds the efficiency analyzer's prompt.
//
// Why not derive from SoC delta? Because Rivian reports BatteryLevelPct
// at 1% resolution (see internal/rivian/ws.go — vs.BatteryLevel.Value
// is integer percent). On a 135 kWh pack that's 1.35 kWh per step. A
// 5-second hard regen at 50 kW recovers ~0.07 kWh = 5% of one step,
// so most braking events don't move the SoC integer at all and are
// invisible to the derivative.
type PowerAnalysis struct {
	// Total energy drawn from the pack across the drive (kWh).
	DrawKwh float64
	// Total energy recovered via regen (kWh, always >= 0).
	RegenKwh float64
	// Regen as a percentage of consumption — 0 when there's no
	// usable signal. Typical EV drives sit in the 10–30 % range;
	// city stop-and-go or long downhill descents push above 40 %.
	RegenPct float64
}

// AnalyzeDrivePower runs the physics model on the windowed sample set
// and returns net draw, regen, and ratio. Returns the zero value when
// the sample set is too thin for a meaningful estimate.
func AnalyzeDrivePower(ss []samples.Sample, d drives.Drive) PowerAnalysis {
	power := derivePower(ss, d.EnergyUsedKWh)
	if len(power) < 2 {
		return PowerAnalysis{}
	}
	var draw, regen float64
	for i := 1; i < len(power); i++ {
		if power[i].Gap {
			continue // no power was modelled across this hole
		}
		dtH := (power[i].T - power[i-1].T) / 3_600_000
		avg := (power[i].Y + power[i-1].Y) / 2
		if avg >= 0 {
			draw += avg * dtH
		} else {
			regen += -avg * dtH
		}
	}
	pct := 0.0
	if draw > 0.1 {
		pct = regen / draw * 100
	}
	return PowerAnalysis{DrawKwh: draw, RegenKwh: regen, RegenPct: pct}
}

// Sample-interval bounds for the physics model.
//
// maxSampleDtSec drops telemetry gaps: a 60 s hole produces a
// deceptively huge dv/dt that reads as a phantom brake event.
//
// minSampleDtSec is the floor, and it matters just as much. The
// recorder's live-merge path writes near-duplicate rows when a WS
// frame and a REST fallback land together, with speeds differing by
// up to 63 mph. This model works in Unix milliseconds (UnixMilli
// below), so pairs finer than 1 ms collapse to dt = 0 and were always
// rejected — but 5,423 recorded intervals sit between 1 ms and 0.5 s
// and were not.
//
// Since fAccel = mass * dv/dt, a 55 mph drop across 1 ms is
// ~24,600 m/s^2, giving a spike near 1e6 kW. Points are emitted at
// interval midpoints, so that spike is then integrated across the
// seconds separating it from its neighbours. When dv is negative the
// whole thing lands in RegenKwh, and the calibration factor is
// clamped to [0.5, 2.0] so it cannot undo an error of that size.
//
// 0.5 s is comfortably below the recorder's 1–5 s driving cadence, so
// no real sample is discarded.
const (
	minSampleDtSec = 0.5
	maxSampleDtSec = 30.0
)

// pkW is one (timestamp_ms, power_kW) datum in the derived series.
type pkW struct {
	T float64 // unix ms
	Y float64 // kW (positive = draw, negative = regen)
	// Gap marks a discontinuity between the previous point and this
	// one: the interval between them was rejected by the bounds
	// above, so no power was modelled across it. Integrators MUST
	// skip such pairs. Points are emitted at interval midpoints, so
	// consecutive entries are otherwise a sample apart; without this
	// flag a 3-minute hole is integrated as though the boundary power
	// persisted throughout it.
	Gap bool
}

// derivePower mirrors derivePower in web/src/lib/power.ts. Vehicle
// constants are R1S Large pack-ish; the calibration loop at the end
// rescales the whole series to match drive.EnergyUsedKWh integrated
// over time, which absorbs per-vehicle variation (different trim,
// aftermarket accessories, cargo, headwind that wasn't in the
// weather data) without hand-tuning every constant.
//
// Convention: positive = draw, negative = regen.
func derivePower(ss []samples.Sample, totalEnergyKwh float64) []pkW {
	if len(ss) < 3 {
		return nil
	}

	const (
		massKg        = 3050.0
		cd            = 0.34
		frontalAreaM2 = 3.0
		rhoKgM3       = 1.225
		crr           = 0.012
		gMS2          = 9.81
		etaDrive      = 0.85
		etaRegen      = 0.70
		accessoryKW   = 1.2
	)

	// Build a smoothed elevation lookup (15 s sigma). Without
	// smoothing the per-sample DEM quantization (~1 m steps from
	// the int16 Terrarium encoding) produces phantom 100 m grades.
	elevPts := buildElevPtsM(ss)
	elevAt := func(tMs float64) float64 {
		if len(elevPts) == 0 {
			return 0
		}
		bestIdx := 0
		bestD := math.Abs(elevPts[0].T - tMs)
		for i := 1; i < len(elevPts); i++ {
			d := math.Abs(elevPts[i].T - tMs)
			if d < bestD {
				bestD = d
				bestIdx = i
			}
		}
		return elevPts[bestIdx].Y // already in meters
	}

	type pt struct {
		t, v, elev float64
	}
	pts := make([]pt, len(ss))
	for i, s := range ss {
		t := float64(s.At.UnixMilli())
		pts[i] = pt{
			t:    t,
			v:    s.SpeedMph * 0.44704, // mph -> m/s
			elev: elevAt(t),
		}
	}

	out := make([]pkW, 0, len(pts))
	// Set once an interval is rejected; stamped onto the next point we
	// actually emit so integrators know the series is discontinuous
	// there. Starts true because the first emitted point has no
	// predecessor to integrate against.
	pendingGap := true
	for i := 1; i < len(pts); i++ {
		dtSec := (pts[i].t - pts[i-1].t) / 1000
		// Reject both ends: telemetry holes AND near-duplicate rows
		// whose dv/dt is numerically explosive. See the constants.
		if dtSec < minSampleDtSec || dtSec > maxSampleDtSec {
			pendingGap = true
			continue
		}
		v := (pts[i].v + pts[i-1].v) / 2
		dvdt := (pts[i].v - pts[i-1].v) / dtSec
		dist := v * dtSec
		dh := pts[i].elev - pts[i-1].elev
		grade := 0.0
		if dist > 0.5 {
			grade = dh / dist
		}

		fAir := 0.5 * cd * frontalAreaM2 * rhoKgM3 * v * v
		fRoll := 0.0
		if v > 0.5 {
			fRoll = crr * massKg * gMS2
		}
		fGrade := massKg * gMS2 * grade
		fAccel := massKg * dvdt
		fTotal := fAir + fRoll + fGrade + fAccel

		pWheels := fTotal * v
		var pBattery float64
		if pWheels >= 0 {
			pBattery = pWheels/etaDrive + accessoryKW*1000
		} else {
			pBattery = pWheels*etaRegen + accessoryKW*1000
		}
		out = append(out, pkW{
			T:   (pts[i].t + pts[i-1].t) / 2,
			Y:   pBattery / 1000,
			Gap: pendingGap,
		})
		pendingGap = false
	}

	// Calibrate to drive.EnergyUsedKWh integrated over time.
	// Clamped to [0.5, 2.0] so a noisy energy total can't produce
	// a wildly distorted estimate.
	if totalEnergyKwh > 0 && len(out) > 1 {
		var energy float64
		for i := 1; i < len(out); i++ {
			if out[i].Gap {
				continue // must match AnalyzeDrivePower, or the
				// factor is derived from an integral the caller
				// never computes
			}
			dtH := (out[i].T - out[i-1].T) / 3_600_000
			avg := (out[i].Y + out[i-1].Y) / 2
			energy += avg * dtH
		}
		if energy > 0.1 {
			factor := totalEnergyKwh / energy
			if factor < 0.5 {
				factor = 0.5
			} else if factor > 2.0 {
				factor = 2.0
			}
			for i := range out {
				out[i].Y *= factor
			}
		}
	}

	// Smooth the power output with a 4 s sigma. Same reasoning as
	// the TS implementation: cleans per-sample dvdt jitter without
	// erasing brake events (which last 5–15 s).
	return gaussianSmooth(out, 4_000)
}

// buildElevPtsM returns smoothed elevation in meters (matching the
// units derivePower works in). The TS version returns feet because
// it doubles as the chart backdrop; here we don't need feet.
func buildElevPtsM(ss []samples.Sample) []pkW {
	pts := make([]pkW, 0, len(ss))
	for _, s := range ss {
		if s.AltitudeM == nil || math.IsNaN(*s.AltitudeM) || math.IsInf(*s.AltitudeM, 0) {
			continue
		}
		pts = append(pts, pkW{T: float64(s.At.UnixMilli()), Y: *s.AltitudeM})
	}
	return gaussianSmooth(pts, 15_000)
}

// gaussianSmooth applies a time-weighted Gaussian moving average
// over the given series. sigmaMs is one standard deviation in
// milliseconds; points within +/- 3 sigma contribute meaningfully.
// Mirrors smoothGaussianTime in web/src/lib/smooth.ts.
func gaussianSmooth(pts []pkW, sigmaMs float64) []pkW {
	if len(pts) < 3 || sigmaMs <= 0 {
		return pts
	}
	cutoff := sigmaMs * 3
	twoSigSq := 2 * sigmaMs * sigmaMs
	out := make([]pkW, len(pts))
	lo, hi := 0, 0
	for i := range pts {
		t := pts[i].T
		for lo < len(pts) && pts[lo].T < t-cutoff {
			lo++
		}
		if hi < lo {
			hi = lo
		}
		for hi < len(pts) && pts[hi].T <= t+cutoff {
			hi++
		}
		var wSum, ySum float64
		for j := lo; j < hi; j++ {
			dt := pts[j].T - t
			w := math.Exp(-(dt * dt) / twoSigSq)
			wSum += w
			ySum += w * pts[j].Y
		}
		// Carry Gap through: smoothing rewrites Y, it must not erase
		// the topology. (Bleed across a gap is negligible anyway —
		// at sigma 4 s the weight 30 s out is e^-28.)
		if wSum > 0 {
			out[i] = pkW{T: t, Y: ySum / wSum, Gap: pts[i].Gap}
		} else {
			out[i] = pts[i]
		}
	}
	return out
}

// SpeedDistribution returns the percentage of drive time spent in
// each speed band. Bands match the route-map color buckets in
// web/src/components/DriveMap.tsx so the prompt and the user's UI
// agree on what "highway" means. Returned in canonical order
// (low to high) so the prompt block reads stop-to-cruise.
type SpeedBand struct {
	Label string
	Pct   float64
}

// SpeedBuckets returns time-weighted percentages by speed band for
// the drive. Returns nil when the sample window is too short to be
// meaningful (< 3 samples or < 30 s of total time).
func SpeedBuckets(ss []samples.Sample) []SpeedBand {
	if len(ss) < 3 {
		return nil
	}
	bounds := []struct {
		max   float64
		label string
	}{
		{5, "<5"},
		{25, "5-25"},
		{50, "25-50"},
		{65, "50-65"},
		{math.Inf(1), "65+"},
	}
	totals := make([]float64, len(bounds))
	var total float64
	for i := 1; i < len(ss); i++ {
		dt := ss[i].At.Sub(ss[i-1].At).Seconds()
		if dt <= 0 || dt > 30 {
			continue
		}
		v := (ss[i].SpeedMph + ss[i-1].SpeedMph) / 2
		for j, b := range bounds {
			if v < b.max {
				totals[j] += dt
				total += dt
				break
			}
		}
	}
	if total < 30 {
		return nil
	}
	out := make([]SpeedBand, 0, len(bounds))
	for i, b := range bounds {
		out = append(out, SpeedBand{
			Label: b.label,
			Pct:   totals[i] / total * 100,
		})
	}
	return out
}

// DriveModeShare is a single (mode, percentage) row computed from
// time spent in each unique drive_mode value across the windowed
// samples. Modes are lowercased and trimmed for stable keys; the
// label stays as-the-mode-arrived for human readability ("All-Purpose"
// vs the lowercased "all-purpose" key).
type DriveModeShare struct {
	Mode string
	Pct  float64
}

// DriveModeShares returns time-share by drive mode, sorted descending
// by share. Empty samples or all-blank-mode inputs return nil so the
// prompt builder can drop the field cleanly.
func DriveModeShares(ss []samples.Sample) []DriveModeShare {
	if len(ss) < 3 {
		return nil
	}
	totals := make(map[string]float64)
	var total float64
	for i := 1; i < len(ss); i++ {
		dt := ss[i].At.Sub(ss[i-1].At).Seconds()
		if dt <= 0 || dt > 30 {
			continue
		}
		mode := ss[i].DriveMode
		if mode == "" {
			continue
		}
		totals[mode] += dt
		total += dt
	}
	if total <= 0 {
		return nil
	}
	out := make([]DriveModeShare, 0, len(totals))
	for k, v := range totals {
		// Humanize wire values ("distance" → "Conserve", etc.) so the
		// share line in the LLM recap prompt uses the same vocabulary
		// the SPA shows. Without this, the recap prompt's instruction
		// to "recommend Conserve only if All-Purpose was used" never
		// lines up with the data line, which says "distance".
		out = append(out, DriveModeShare{Mode: humanizeDriveMode(k), Pct: v / total * 100})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pct > out[j].Pct })
	return out
}

// CabinTempStats summarizes interior temperature across a drive.
// Returns nil flag when no usable cabin samples exist (filtered out
// the same way the SPA tooltip does — zero readings are sentinels
// from the live merge path when Rivian's WS feed didn't carry a
// fresh ambient value).
type CabinTempStats struct {
	MedianCabinC   float64
	MedianOutsideC float64
	MedianDeltaC   float64 // cabin - outside, when both are present per sample
	HasDelta       bool
}

func CabinTemp(ss []samples.Sample) (CabinTempStats, bool) {
	cabin := make([]float64, 0, len(ss))
	outside := make([]float64, 0, len(ss))
	deltas := make([]float64, 0, len(ss))
	for _, s := range ss {
		hasIn := s.InsideTempC != 0 && !math.IsNaN(s.InsideTempC)
		hasOut := s.OutsideTempC != 0 && !math.IsNaN(s.OutsideTempC)
		if hasIn {
			cabin = append(cabin, s.InsideTempC)
		}
		if hasOut {
			outside = append(outside, s.OutsideTempC)
		}
		if hasIn && hasOut {
			deltas = append(deltas, s.InsideTempC-s.OutsideTempC)
		}
	}
	if len(cabin) == 0 {
		return CabinTempStats{}, false
	}
	st := CabinTempStats{
		MedianCabinC: medianFloat(cabin),
	}
	if len(outside) > 0 {
		st.MedianOutsideC = medianFloat(outside)
	}
	if len(deltas) > 0 {
		st.MedianDeltaC = medianFloat(deltas)
		st.HasDelta = true
	}
	return st, true
}

// StopStats counts low-speed dwell events (a "stop") and total time
// spent below 5 mph. A "stop" is an interval of consecutive samples
// with avg speed < 5 mph that lasts at least 8 s — short enough to
// catch traffic-light waits, long enough to skip momentary GPS
// hiccups during steady cruise.
type StopStats struct {
	Count     int
	IdleSec   float64
	HasSignal bool
}

func Stops(ss []samples.Sample) StopStats {
	if len(ss) < 3 {
		return StopStats{}
	}
	out := StopStats{HasSignal: true}
	const stopThresholdMph = 5.0
	const minStopSec = 8.0
	var inStop bool
	var stopStart float64
	var totalIdle float64
	for i := 1; i < len(ss); i++ {
		dt := ss[i].At.Sub(ss[i-1].At).Seconds()
		if dt <= 0 || dt > 30 {
			// Treat large gaps as a forced exit from any
			// in-progress stop so we don't credit minutes
			// of telemetry blackout to the same stop event.
			inStop = false
			continue
		}
		v := (ss[i].SpeedMph + ss[i-1].SpeedMph) / 2
		if v < stopThresholdMph {
			totalIdle += dt
			if !inStop {
				inStop = true
				stopStart = float64(ss[i-1].At.UnixMilli())
			}
		} else if inStop {
			elapsedSec := (float64(ss[i-1].At.UnixMilli()) - stopStart) / 1000
			if elapsedSec >= minStopSec {
				out.Count++
			}
			inStop = false
		}
	}
	out.IdleSec = totalIdle
	return out
}

func medianFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}
