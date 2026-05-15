package tripweather

import (
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/weather"
)

func TestMultiplier_NilAndPassthrough(t *testing.T) {
	if m := Multiplier(nil); m != 1.0 {
		t.Errorf("nil snapshot: got %v want 1.0", m)
	}
	// Neutral conditions: 21C, no wind, no precip.
	s := &weather.Snapshot{HasApparent: true, ApparentTempC: 21, HasHeadwind: true, HeadwindKPH: 0}
	if m := Multiplier(s); m != 1.0 {
		t.Errorf("neutral: got %v want 1.0", m)
	}
}

func TestMultiplier_ColdDerate(t *testing.T) {
	// 0C apparent → 1 + 0.008 * 21 = 1.168
	s := &weather.Snapshot{HasApparent: true, ApparentTempC: 0}
	if got := roundTo(Multiplier(s), 3); got != 1.168 {
		t.Errorf("0C: got %v want 1.168", got)
	}
	// -7C apparent → 1 + 0.008 * 28 = 1.224 (matches the ~22% loss anchor)
	s.ApparentTempC = -7
	if got := roundTo(Multiplier(s), 3); got != 1.224 {
		t.Errorf("-7C: got %v want 1.224", got)
	}
	// Falls back to TempC when ApparentTempC isn't reported.
	s2 := &weather.Snapshot{HasTemp: true, TempC: 0}
	if got := roundTo(Multiplier(s2), 3); got != 1.168 {
		t.Errorf("TempC fallback: got %v want 1.168", got)
	}
}

func TestMultiplier_HeatDerate(t *testing.T) {
	// 37C → 1 + 0.004 * 10 = 1.04 (A/C is cheaper than cabin heat — by design).
	s := &weather.Snapshot{HasApparent: true, ApparentTempC: 37}
	if got := roundTo(Multiplier(s), 3); got != 1.04 {
		t.Errorf("37C: got %v want 1.04", got)
	}
	// 27C is the boundary — exactly at the threshold should pass through.
	s.ApparentTempC = 27
	if got := Multiplier(s); got != 1.0 {
		t.Errorf("27C boundary: got %v want 1.0", got)
	}
}

func TestMultiplier_HeadwindAndTailwind(t *testing.T) {
	// +20 kph headwind → 1 + 0.012 * 20 = 1.24
	s := &weather.Snapshot{HasHeadwind: true, HeadwindKPH: 20}
	if got := roundTo(Multiplier(s), 3); got != 1.24 {
		t.Errorf("headwind 20: got %v want 1.24", got)
	}
	// -20 kph (tailwind) → 1 - 0.012 * 20 = 0.76
	s.HeadwindKPH = -20
	if got := roundTo(Multiplier(s), 3); got != 0.76 {
		t.Errorf("tailwind 20: got %v want 0.76", got)
	}
	// Clamped at ±30: a 50 kph headwind reads as 30.
	s.HeadwindKPH = 50
	if got := roundTo(Multiplier(s), 3); got != 1.36 {
		t.Errorf("headwind clamp: got %v want 1.36", got)
	}
}

func TestMultiplier_Precip(t *testing.T) {
	// 0.3 mm is below threshold — no adjustment.
	s := &weather.Snapshot{HasPrecip: true, PrecipMM: 0.3}
	if got := Multiplier(s); got != 1.0 {
		t.Errorf("trace precip: got %v want 1.0", got)
	}
	// 1.0 mm crosses the on/off threshold.
	s.PrecipMM = 1.0
	if got := roundTo(Multiplier(s), 3); got != 1.05 {
		t.Errorf("wet: got %v want 1.05", got)
	}
}

func TestMultiplier_Compounds(t *testing.T) {
	// 0C apparent + 20 kph head + wet → 1.168 * 1.24 * 1.05 ≈ 1.521
	s := &weather.Snapshot{
		HasApparent: true, ApparentTempC: 0,
		HasHeadwind: true, HeadwindKPH: 20,
		HasPrecip: true, PrecipMM: 2,
	}
	got := Multiplier(s)
	want := 1.168 * 1.24 * 1.05
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("compound: got %v want ~%v", got, want)
	}
}

func TestMultiplier_Floor(t *testing.T) {
	// Pathological tailwind alone can't reach 0.7 (min is 0.64), but a
	// hot tailwind day shouldn't go free-energy below 0.7.
	s := &weather.Snapshot{HasHeadwind: true, HeadwindKPH: -30}
	if got := Multiplier(s); got < 0.7 || got > 0.7+1e-9 {
		// Verify floor pin at exactly 0.7 — math says 1 - 0.012*30 = 0.64.
		if got != 0.7 {
			t.Errorf("floor: got %v want 0.7", got)
		}
	}
}

// TestAdjust_HappyPath: a two-leg plan, both legs into a 15 kph head
// at 0C, target 20% — corrected arrival should drop noticeably below
// Rivian's planned arrival and trip BelowTarget when the multiplier
// eats through the margin.
func TestAdjust_HappyPath(t *testing.T) {
	cold := &weather.Snapshot{HasApparent: true, ApparentTempC: 0, HasHeadwind: true, HeadwindKPH: 15}
	legs := []Leg{{Weather: cold}, {Weather: cold}}
	// Rivian planned: 80 → 50 (drop 30) → 25 (drop 25). Target 20%.
	adj := Adjust(80, []float64{50, 25}, legs, 20)
	if adj == nil {
		t.Fatal("Adjust returned nil")
	}
	mult := Multiplier(cold) // 1.168 * 1.18 = ~1.378
	if math.Abs(adj.Multipliers[0]-mult) > 1e-6 || math.Abs(adj.Multipliers[1]-mult) > 1e-6 {
		t.Errorf("multipliers: %v want both ~%v", adj.Multipliers, mult)
	}
	wantSoc1 := 80 - 30*mult
	wantSoc2 := wantSoc1 - 25*mult
	if math.Abs(adj.AdjustedArrivalSoC[0]-wantSoc1) > 1e-6 {
		t.Errorf("waypoint 1 SoC: %v want %v", adj.AdjustedArrivalSoC[0], wantSoc1)
	}
	if math.Abs(adj.AdjustedArrivalSoC[1]-wantSoc2) > 1e-6 {
		t.Errorf("waypoint 2 SoC: %v want %v", adj.AdjustedArrivalSoC[1], wantSoc2)
	}
	if adj.FinalArrivalSoC != adj.AdjustedArrivalSoC[1] {
		t.Errorf("FinalArrivalSoC mismatch")
	}
	if !adj.BelowTarget {
		t.Errorf("expected BelowTarget=true (final %v < 20)", adj.FinalArrivalSoC)
	}
}

// TestAdjust_NeutralWeatherPassThrough: a plan with neutral weather
// (mult=1) on every leg must produce exactly Rivian's planned SoCs.
func TestAdjust_NeutralWeatherPassThrough(t *testing.T) {
	mild := &weather.Snapshot{HasApparent: true, ApparentTempC: 21}
	adj := Adjust(80, []float64{60, 40}, []Leg{{Weather: mild}, {Weather: mild}}, 20)
	if adj.AdjustedArrivalSoC[0] != 60 || adj.AdjustedArrivalSoC[1] != 40 {
		t.Errorf("pass-through broken: %v", adj.AdjustedArrivalSoC)
	}
	if adj.BelowTarget {
		t.Errorf("BelowTarget should be false when final 40 ≥ target 20")
	}
}

// TestAdjust_ChargingLegPassThrough: Rivian's response carries
// post-charging arrival SoCs at charger waypoints — those legs have
// arr > prev (e.g. 30 → 80). Multiplying that "delta" would amplify
// the charge to 65 percentage points instead of 50; the code must
// pass charging deltas through verbatim.
func TestAdjust_ChargingLegPassThrough(t *testing.T) {
	cold := &weather.Snapshot{HasApparent: true, ApparentTempC: 0}
	// 80 → 30 (drive, drop 50, corrected) → 80 (charge, arrives at 80) → 20 (drive)
	adj := Adjust(80, []float64{30, 80, 20}, []Leg{{Weather: cold}, {Weather: cold}, {Weather: cold}}, 15)
	if adj.AdjustedArrivalSoC[1] != 80 {
		t.Errorf("charging stop got multiplied: %v want 80", adj.AdjustedArrivalSoC[1])
	}
	// Final leg: prev=80, arr=20, delta=60, mult=1.168 → 80 - 60*1.168 = 9.92
	want := 80 - 60*Multiplier(cold)
	if math.Abs(adj.AdjustedArrivalSoC[2]-want) > 1e-6 {
		t.Errorf("post-charge leg: %v want %v", adj.AdjustedArrivalSoC[2], want)
	}
}

// TestAdjust_NilLegWeather: a leg with no weather data uses
// multiplier 1.0 (pass-through). This is the real-world fallback
// when Open-Meteo fails on one leg but succeeds on others.
func TestAdjust_NilLegWeather(t *testing.T) {
	cold := &weather.Snapshot{HasApparent: true, ApparentTempC: 0}
	// Two-leg plan: leg 1 has cold weather, leg 2 has no data.
	adj := Adjust(80, []float64{50, 25}, []Leg{{Weather: cold}, {Weather: nil}}, 20)
	if adj.Multipliers[1] != 1.0 {
		t.Errorf("nil-weather leg should be 1.0, got %v", adj.Multipliers[1])
	}
	// Leg 1 corrected, leg 2 verbatim.
	want1 := 80 - 30*Multiplier(cold)
	want2 := want1 - 25
	if math.Abs(adj.AdjustedArrivalSoC[0]-want1) > 1e-6 {
		t.Errorf("leg 1: %v want %v", adj.AdjustedArrivalSoC[0], want1)
	}
	if math.Abs(adj.AdjustedArrivalSoC[1]-want2) > 1e-6 {
		t.Errorf("leg 2: %v want %v", adj.AdjustedArrivalSoC[1], want2)
	}
}

// TestAdjust_NegativeFloor: a plan that would drop below 0% (legs +
// cold derate exceed pack) must clamp at 0, not produce negative SoC.
func TestAdjust_NegativeFloor(t *testing.T) {
	cold := &weather.Snapshot{HasApparent: true, ApparentTempC: -20}
	adj := Adjust(50, []float64{10}, []Leg{{Weather: cold}}, 5)
	if adj.AdjustedArrivalSoC[0] < 0 {
		t.Errorf("SoC went negative: %v", adj.AdjustedArrivalSoC[0])
	}
	if !adj.BelowTarget {
		t.Errorf("expected BelowTarget=true")
	}
}

// TestAdjust_LengthMismatchReturnsNil: defensive — a caller that
// passes mismatched lengths must get nil back, not garbage.
func TestAdjust_LengthMismatchReturnsNil(t *testing.T) {
	if adj := Adjust(80, []float64{50, 25}, []Leg{{}}, 20); adj != nil {
		t.Errorf("mismatched lengths should return nil, got %+v", adj)
	}
}
