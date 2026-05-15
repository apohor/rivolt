// Package tripweather corrects a Rivian trip plan's per-waypoint SoC
// curve for weather conditions Rivian's planner doesn't see. The
// gateway models pack + base efficiency from the account-side vehicle
// config, but its `planTripWithMultiStopV2` operation accepts no
// weather inputs — so a cold day or a strong headwind silently turns
// a "comfortable 5% arrival margin" plan into a stranded one.
//
// Adjust takes the plan's waypoint sequence plus a parallel slice of
// per-leg weather snapshots, applies an energy multiplier per leg,
// and re-runs the cumulative SoC subtraction. The output mirrors the
// input shape (corrected ArrivalSoC per waypoint) plus a top-level
// flag for whether the corrected final arrival is below the user's
// target floor.
//
// The multiplier model lives here as pure functions so it can be
// unit-tested without booting Postgres or hitting Open-Meteo. See
// docs/ROADMAP.md "Weather-aware range adjustment" for what's in v1
// and what's deferred.
package tripweather

import (
	"math"

	"github.com/apohor/rivolt/internal/weather"
)

// Leg is the input shape for one driving segment between two
// waypoints. Multiplier is computed from Weather; callers may pass
// a nil Weather to signal "no snapshot available" — that leg gets a
// pass-through multiplier of 1.0.
type Leg struct {
	Weather *weather.Snapshot
}

// Adjustment is the output. AdjustedArrivalSoC[i] is the corrected
// SoC at waypoint i, in percent. Multipliers[i] is the leg multiplier
// for the leg ending at waypoint i+1 (so Multipliers has len == len-1
// of the waypoint slice). For the origin waypoint, AdjustedArrivalSoC
// equals the input StartingSoC.
type Adjustment struct {
	AdjustedArrivalSoC []float64
	Multipliers        []float64
	// BelowTarget is true when the final corrected arrival SoC is
	// below TargetArrivalSoCPercent. Caller renders the warning chip
	// based on this single boolean — keeps the SPA decision local.
	BelowTarget bool
	// FinalArrivalSoC is a convenience alias for the last entry of
	// AdjustedArrivalSoC so callers don't have to index.
	FinalArrivalSoC float64
}

// Multiplier returns the per-leg energy multiplier for the given
// weather snapshot. A return value of 1.0 means "no adjustment";
// 1.15 means "this leg needs 15% more energy than Rivian planned".
//
// Model (see docs/ROADMAP.md for the underlying empirical sources):
//
//	cold:    1 + 0.008 * max(0, 21 - apparent_temp_C)   // 30% loss at -7C
//	heat:    1 + 0.004 * max(0, apparent_temp_C - 27)   // A/C draw
//	wind:    1 + 0.012 * clamp(headwind_kph, -30, 30)   // drag, symmetric
//	wet:     × 1.05 when precip_mm > 0.5                // coarse on/off
//
// Factors are multiplicative so a cold + headwind + wet leg compounds.
// nil snapshot returns 1.0.
func Multiplier(s *weather.Snapshot) float64 {
	if s == nil {
		return 1.0
	}
	m := 1.0
	// Prefer apparent temp (captures wind-chill) over raw; fall back
	// to TempC when ApparentTempC isn't reported.
	temp, hasTemp := 0.0, false
	switch {
	case s.HasApparent:
		temp, hasTemp = s.ApparentTempC, true
	case s.HasTemp:
		temp, hasTemp = s.TempC, true
	}
	if hasTemp {
		if temp < 21 {
			m *= 1 + 0.008*(21-temp)
		}
		if temp > 27 {
			m *= 1 + 0.004*(temp-27)
		}
	}
	if s.HasHeadwind {
		hw := s.HeadwindKPH
		if hw > 30 {
			hw = 30
		} else if hw < -30 {
			hw = -30
		}
		m *= 1 + 0.012*hw
	}
	if s.HasPrecip && s.PrecipMM > 0.5 {
		m *= 1.05
	}
	// Floor at 0.7 — pathological combinations of cold + tailwind
	// otherwise produce free-energy multipliers that aren't real.
	if m < 0.7 {
		m = 0.7
	}
	return m
}

// Adjust re-runs the cumulative SoC subtraction with per-leg
// multipliers applied. startingSoC + waypointArrivalSoC describe the
// uncorrected plan; len(legs) must equal len(waypointArrivalSoC)
// (one leg per arrival), and waypointArrivalSoC[i] is the SoC
// Rivian forecast at waypoint i+1 (origin is implied).
//
// targetArrivalSoC is the user's floor; the returned BelowTarget is
// true when the corrected final arrival is below it.
func Adjust(startingSoC float64, waypointArrivalSoC []float64, legs []Leg, targetArrivalSoC float64) *Adjustment {
	if len(legs) != len(waypointArrivalSoC) {
		return nil
	}
	out := &Adjustment{
		AdjustedArrivalSoC: make([]float64, len(waypointArrivalSoC)),
		Multipliers:        make([]float64, len(legs)),
	}
	soc := startingSoC
	prev := startingSoC
	for i, arr := range waypointArrivalSoC {
		delta := prev - arr // SoC drop on this leg
		// Charging stops produce arr > prev (Rivian's arrival here is
		// post-charging in some response shapes). Don't multiply a
		// negative delta — that would amplify the charge instead of
		// the consumption.
		mult := Multiplier(legs[i].Weather)
		out.Multipliers[i] = mult
		if delta > 0 {
			soc -= delta * mult
		} else {
			soc = arr // pass through charging deltas verbatim
		}
		if soc < 0 {
			soc = 0
		}
		out.AdjustedArrivalSoC[i] = soc
		prev = arr
	}
	out.FinalArrivalSoC = out.AdjustedArrivalSoC[len(out.AdjustedArrivalSoC)-1]
	out.BelowTarget = out.FinalArrivalSoC < targetArrivalSoC
	return out
}

// roundTo is a small helper used in tests to compare floats with a
// finite precision; not exported because callers should use
// reflect.DeepEqual via the test helpers in adjust_test.go.
func roundTo(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}
