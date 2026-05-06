package recap

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// EfficiencyFactor is a single contributor to efficiency variance,
// scored by the model. Negative impacts mean it hurt efficiency,
// positive means it helped.
type EfficiencyFactor struct {
	Name              string  `json:"name"`
	ImpactEstimatePct float64 `json:"impact_estimate_pct"`
	Confidence0To100  int     `json:"confidence_0_to_100"`
}

// EfficiencyParsed is the structured shape the model returns. Every
// field is optional; the SPA falls back to Analysis (raw text) when
// parsing fails.
type EfficiencyParsed struct {
	Factors        []EfficiencyFactor `json:"factors,omitempty"`
	Recommendation string             `json:"recommendation,omitempty"`
	Forecast       string             `json:"forecast,omitempty"`
	Summary        string             `json:"summary,omitempty"`
}

// EfficiencyResult is what GenerateEfficiency returns to the handler.
type EfficiencyResult struct {
	Analysis     string
	Parsed       *EfficiencyParsed
	Model        string
	InputTokens  int64
	OutputTokens int64
}

// EfficiencyInputs is the bag of data the prompt builder consumes.
type EfficiencyInputs struct {
	Drive            drives.Drive
	Samples          []samples.Sample
	UseFahrenheit    bool
	BaselineMiPerKWh float64
	BaselineDays     int
	Weather          *Weather

	// Vehicle profile (per-vehicle, set in Settings). Nil/empty
	// fields are silently dropped from the prompt.
	VehicleProfile *VehicleProfile

	// Per-trip transient context, populated from the request body
	// on the efficiency POST. ExtraLoadLb is in pounds; Towing is
	// a hint that an external load (trailer, hauler) was attached.
	ExtraLoadLb float64
	Towing      bool
}

// VehicleProfile is the prompt-side view of the user's per-vehicle
// settings. Mirrors db.VehicleProfile but kept local so the recap
// package doesn't depend on the db package.
type VehicleProfile struct {
	TireType           string
	WheelInches        int
	Accessories        []string
	DefaultExtraLoadLb float64
	FrequentlyTows     bool
}

// GenerateEfficiency calls the analyzer with a system+user prompt and
// returns the parsed result. Caller controls the timeout via ctx.
func GenerateEfficiency(ctx context.Context, a *ai.Analyzer, in EfficiencyInputs) (EfficiencyResult, error) {
	if a == nil {
		return EfficiencyResult{}, fmt.Errorf("nil analyzer")
	}
	system, user := buildEfficiencyPrompt(in)
	reply, usage, err := a.Complete(ctx, system, user)
	if err != nil {
		return EfficiencyResult{}, err
	}
	out := EfficiencyResult{
		Analysis:     strings.TrimSpace(reply),
		Parsed:       ParseEfficiency(reply),
		Model:        a.ModelName(),
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}
	return out, nil
}

// ParseEfficiency tolerates ```json fences and leading/trailing
// whitespace around the JSON envelope. Returns nil on parse failure.
func ParseEfficiency(s string) *EfficiencyParsed {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	if strings.HasPrefix(t, "```") {
		// Strip ```json ... ``` or ``` ... ``` fences.
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		if i := strings.LastIndex(t, "```"); i >= 0 {
			t = t[:i]
		}
		t = strings.TrimSpace(t)
	}
	// Some models return a leading prose sentence; find the first '{'.
	if i := strings.Index(t, "{"); i > 0 {
		t = t[i:]
	}
	var p EfficiencyParsed
	if err := json.Unmarshal([]byte(t), &p); err != nil {
		return nil
	}
	if len(p.Factors) == 0 && p.Recommendation == "" && p.Forecast == "" && p.Summary == "" {
		return nil
	}
	return &p
}

// buildEfficiencyPrompt constructs the system + user prompts. Pure
// function, side-effect-free, deterministic for fixed inputs.
func buildEfficiencyPrompt(in EfficiencyInputs) (string, string) {
	const system = `You are an EV efficiency coach. Analyze a single drive and explain why efficiency landed where it did.

Break down the variance by factor: weather, terrain, driving style, climate control, payload, route, tire pressure, tire type, wheel size, towing, accessories. For each factor estimate impact in percent (negative = hurt efficiency, positive = helped) and confidence 0-100.

Use the vehicle profile and per-trip context (towing, extra load, tire pressure, accessories) when present. Typical reference points (use as priors only; weight by your confidence):
- All-terrain tires: -5 to -10% vs all-season; winter tires: -3 to -7%.
- 22-inch wheels: -2 to -4% vs 20-inch.
- Roof rack empty: -2 to -5%; with cargo box: -8 to -15% at highway speed.
- Bike rack on hitch: -5 to -10% at highway speed.
- Towing: -25 to -50% depending on trailer profile and speed.
- Tire pressure 5 psi below placard: -2 to -3%.
- 500 lb extra payload: -1 to -2%.

Then suggest ONE specific, actionable change the driver can make on their NEXT drive. Be concrete (e.g. "set climate to 70F instead of 65F to save ~3% range") not vague ("drive more carefully").

Respond ONLY with a JSON object matching this shape, no prose outside the JSON:
{
  "factors": [
    {"name": "Headwind", "impact_estimate_pct": -8, "confidence_0_to_100": 75},
    {"name": "Cold cabin heating", "impact_estimate_pct": -5, "confidence_0_to_100": 60}
  ],
  "recommendation": "Pre-condition the cabin while still plugged in to save ~4% on heating draw next cold drive.",
  "forecast": "Following this could improve efficiency ~4-6% on similar trips.",
  "summary": "Cold weather and headwind cost roughly 13% efficiency; pre-conditioning recovers most of the heating loss."
}`

	var b strings.Builder
	d := in.Drive
	dur := d.EndedAt.Sub(d.StartedAt)
	miPerKWh := 0.0
	if d.EnergyUsedKWh > 0 {
		miPerKWh = d.DistanceMi / d.EnergyUsedKWh
	}
	avgMph := d.AvgSpeedMph
	maxMph := d.MaxSpeedMph

	fmt.Fprintf(&b, "Drive on %s\n", d.StartedAt.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "Distance: %.1f mi\n", d.DistanceMi)
	fmt.Fprintf(&b, "Duration: %s\n", roundDuration(dur))
	fmt.Fprintf(&b, "Avg speed: %.0f mph (peak %.0f mph)\n", avgMph, maxMph)
	if d.EnergyUsedKWh > 0 {
		fmt.Fprintf(&b, "Energy used: %.2f kWh\n", d.EnergyUsedKWh)
		fmt.Fprintf(&b, "Efficiency: %.2f mi/kWh\n", miPerKWh)
	}
	if in.BaselineMiPerKWh > 0 {
		delta := 0.0
		if miPerKWh > 0 {
			delta = (miPerKWh - in.BaselineMiPerKWh) / in.BaselineMiPerKWh * 100
		}
		fmt.Fprintf(&b, "Baseline (last %d days): %.2f mi/kWh (this drive: %+.1f%% vs baseline)\n",
			in.BaselineDays, in.BaselineMiPerKWh, delta)
	}
	fmt.Fprintf(&b, "SoC: %.0f%% -> %.0f%%\n", d.StartSoCPct, d.EndSoCPct)

	// Drive modes encountered
	if modes := summarizeDriveModes(in.Samples); modes != "" {
		fmt.Fprintf(&b, "Drive modes used: %s\n", modes)
	}

	// Elevation (reuse helper from recap.go)
	if elev := summarizeElevation(in.Samples); elev.has {
		fmt.Fprintf(&b, "Elevation: %s (climb %d ft, descent %d ft, net %+d ft)\n",
			elev.profileLabel(), int(math.Round(elev.climbFt)), int(math.Round(elev.descentFt)), int(math.Round(elev.netFt)))
	}

	// Outside temperature from samples
	if t := medianNonZeroOutsideTempC(in.Samples); t != nil {
		if in.UseFahrenheit {
			fmt.Fprintf(&b, "Outside temp (median): %.0f F\n", *t*1.8+32)
		} else {
			fmt.Fprintf(&b, "Outside temp (median): %.0f C\n", *t)
		}
	}

	// Weather block
	if w := in.Weather; w != nil {
		b.WriteString("Weather at start: ")
		parts := []string{}
		if w.HasTemp {
			if in.UseFahrenheit {
				parts = append(parts, fmt.Sprintf("%.0f F", w.TempC*1.8+32))
			} else {
				parts = append(parts, fmt.Sprintf("%.0f C", w.TempC))
			}
		}
		if w.HasConditions && w.Conditions != "" {
			parts = append(parts, w.Conditions)
		}
		if w.HasWind {
			mph := w.WindKPH * 0.621371
			parts = append(parts, fmt.Sprintf("%.0f mph wind", mph))
		}
		if w.HasHeadwind {
			mph := w.HeadwindKPH * 0.621371
			if mph > 0 {
				parts = append(parts, fmt.Sprintf("%.0f mph headwind", mph))
			} else if mph < 0 {
				parts = append(parts, fmt.Sprintf("%.0f mph tailwind", -mph))
			}
		}
		if w.HasPrecip && w.PrecipMM > 0 {
			parts = append(parts, fmt.Sprintf("%.2f in precip", w.PrecipMM/25.4))
		}
		if w.HasHumidity {
			parts = append(parts, fmt.Sprintf("%.0f%% humidity", w.HumidityPct))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n")
	}

	// Tire pressure (median of windowed samples). Min-of-4 is what
	// got persisted, so this is "median worst tire across the drive".
	if psi := medianTirePressurePSI(in.Samples); psi > 0 {
		fmt.Fprintf(&b, "Tire pressure (median min corner): %.0f psi\n", psi)
	}

	// Per-vehicle profile (set in Settings).
	if vp := in.VehicleProfile; vp != nil {
		var bits []string
		if vp.TireType != "" {
			bits = append(bits, fmt.Sprintf("tire type %s", strings.ReplaceAll(vp.TireType, "_", "-")))
		}
		if vp.WheelInches > 0 {
			bits = append(bits, fmt.Sprintf("%d in wheels", vp.WheelInches))
		}
		if len(vp.Accessories) > 0 {
			bits = append(bits, "accessories: "+strings.Join(vp.Accessories, ", "))
		}
		if vp.DefaultExtraLoadLb > 0 {
			bits = append(bits, fmt.Sprintf("default extra load %.0f lb", vp.DefaultExtraLoadLb))
		}
		if vp.FrequentlyTows {
			bits = append(bits, "frequently tows")
		}
		if len(bits) > 0 {
			fmt.Fprintf(&b, "Vehicle profile: %s\n", strings.Join(bits, "; "))
		}
	}

	// Per-trip transient context.
	var trip []string
	if in.ExtraLoadLb > 0 {
		trip = append(trip, fmt.Sprintf("extra load %.0f lb (this trip)", in.ExtraLoadLb))
	}
	if in.Towing {
		trip = append(trip, "towing this trip")
	}
	if len(trip) > 0 {
		fmt.Fprintf(&b, "Trip context: %s\n", strings.Join(trip, "; "))
	}

	return system, b.String()
}

// summarizeDriveModes returns a comma-separated list of unique drive
// modes observed in samples (lowercased, deduped). Empty when no
// samples carry a mode.
func summarizeDriveModes(ss []samples.Sample) string {
	seen := make(map[string]struct{})
	out := []string{}
	for _, s := range ss {
		m := strings.TrimSpace(strings.ToLower(s.DriveMode))
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return strings.Join(out, ", ")
}

// medianTirePressurePSI returns the median of TirePressureMinBar
// across non-nil samples, converted to psi (1 bar ~= 14.5038 psi).
// Returns 0 when no samples carry a reading.
func medianTirePressurePSI(ss []samples.Sample) float64 {
	bars := make([]float64, 0, len(ss))
	for _, s := range ss {
		if s.TirePressureMinBar == nil || *s.TirePressureMinBar <= 0 {
			continue
		}
		bars = append(bars, *s.TirePressureMinBar)
	}
	if len(bars) == 0 {
		return 0
	}
	// Insertion sort is fine: typical drive yields a few hundred
	// samples, dominated by the JSON marshal cost downstream.
	for i := 1; i < len(bars); i++ {
		v := bars[i]
		j := i - 1
		for j >= 0 && bars[j] > v {
			bars[j+1] = bars[j]
			j--
		}
		bars[j+1] = v
	}
	mid := bars[len(bars)/2]
	return mid * 14.5037738
}
