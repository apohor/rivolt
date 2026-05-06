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
//
// MagnitudeKWh and Evidence are newer schema fields:
//   - MagnitudeKWh: signed kWh impact on this specific drive. Lets
//     the SPA show concrete loss in addition to the relative percent
//     and lets us sum factors and sanity-check against EnergyUsedKWh.
//   - Evidence: short string citing the data point that justified
//     the factor (e.g. "Headwind 18 mph from Open-Meteo at start").
//     Forces the model to commit to a citation; the SPA renders it
//     beneath the factor name as a verifiable subtitle.
//
// Both are optional in the JSON wire format and on legacy stored
// rows so the SPA renders defensively without breaking older
// analyses generated before the field landed.
type EfficiencyFactor struct {
	Name              string  `json:"name"`
	ImpactEstimatePct float64 `json:"impact_estimate_pct"`
	Confidence0To100  int     `json:"confidence_0_to_100"`
	MagnitudeKWh      float64 `json:"magnitude_kwh,omitempty"`
	Evidence          string  `json:"evidence,omitempty"`
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
	// TirePlacardPSI is the user-set door-jamb cold-fill pressure.
	// Zero means unset — the prompt then sends raw psi and lets the
	// model use its training-data prior for placard.
	TirePlacardPSI float64
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

Break down the variance by factor: weather, terrain, driving style, climate control, payload, route, tire pressure, tire type, wheel size, towing, accessories.

Typical reference points (use as priors only; weight by your confidence):
- All-terrain tires: -5 to -10% vs all-season; winter tires: -3 to -7%.
- 22-inch wheels: -2 to -4% vs 20-inch.
- Roof rack empty: -2 to -5%; with cargo box: -8 to -15% at highway speed.
- Bike rack on hitch: -5 to -10% at highway speed.
- Towing: -25 to -50% depending on trailer profile and speed.
- Tire pressure 5 psi below placard: -2 to -3%.
- 500 lb extra payload: -1 to -2%.

CONFIDENCE RUBRIC — use these anchors strictly:
- 90-100: Directly observed in this trip's data (e.g. headwind 18 mph when the weather block reports it).
- 60-89:  Inferable from two or more data points (e.g. cabin heating from outside 28F + 35-min drive + median cabin 72F).
- 30-59:  Prior-based, applied generically to this vehicle/trip (e.g. 22-inch wheels from the vehicle profile).
- 0-29:   Speculative. If you would assign <30, OMIT the factor instead.

FACTOR RULES:
- Emit AT MOST 5 factors. Three well-supported factors beat five weak ones.
- Each factor's existence and magnitude must be defensible by a number elsewhere in this prompt or in the vehicle profile. If you can't point to the supporting data, omit the factor.
- Use canonical factor names where they fit:
    Headwind, Tailwind, Cold cabin heating, Hot cabin cooling, Climb,
    Descent, Highway speed, City stop-and-go, Aggressive acceleration,
    Smooth cruising, Roof rack, Cargo box, Bike rack, Hitch rack,
    Towing, Heavy payload, Low tire pressure, All-terrain tires,
    Winter tires, Large wheels, Precipitation, Wet roads, Cold-start HVAC.
  If a factor doesn't fit any of these, name it concisely (<= 3 words).
- For each factor emit:
    impact_estimate_pct  signed % (negative = hurt; positive = helped)
    magnitude_kwh        signed kWh on this trip (negative = hurt). Should approximately sum to the trip's deviation from the baseline.
    confidence_0_to_100  per the rubric above.
    evidence             <= 80 chars citing the specific data point that justified this factor (e.g. "Headwind 18 mph at start"). Required.

ANTI-HALLUCINATION:
- Do not invent values not present in this prompt. If wind isn't given, don't claim wind. If outside temp isn't given, don't claim cold-weather effects.
- When citing a number in the summary, recommendation, or evidence, it must appear (verbatim or as a direct rounding) in the user prompt below.
- If the data is too thin (no efficiency, no elevation, no weather, fewer than ~5 minutes of samples), return an empty factors array, summary "Not enough data for a confident analysis", and a recommendation pointing the user at a longer drive.

RECOMMENDATION CONSTRAINTS:
- Use the user's chosen temperature unit — the user prompt below writes temperatures in F or C; match it.
- Rivolt does NOT capture the one-pedal driving setting or regen-level setting. Do not recommend toggling either. Do not assert what the driver currently has set.
- Only recommend a drive-mode change when the "Drive mode share" line is present AND the change is clearly supported (e.g. recommend Conserve only if the data shows All-Purpose was used for a steady-state highway trip). If the field is absent or the trip already used the recommended mode, pick a different recommendation.
- The recommendation must address one of the factors you listed (mention it by name or by the same data signal that justified it).
- Anchor the recommendation to a specific number from this trip's data.

LENGTH BUDGET:
- summary: <= 2 sentences.
- recommendation: <= 1 sentence.
- forecast: <= 1 sentence.

Respond ONLY with a JSON object matching this shape, no prose outside the JSON:
{
  "factors": [
    {
      "name": "Headwind",
      "impact_estimate_pct": -8,
      "magnitude_kwh": -0.7,
      "confidence_0_to_100": 85,
      "evidence": "18 mph headwind from Open-Meteo at start"
    },
    {
      "name": "Cold cabin heating",
      "impact_estimate_pct": -5,
      "magnitude_kwh": -0.4,
      "confidence_0_to_100": 70,
      "evidence": "Outside 28F, cabin 72F, 35-min drive"
    }
  ],
  "recommendation": "Pre-condition the cabin while still plugged in to save ~4% on heating draw next cold drive.",
  "forecast": "Following this could improve efficiency 4-6% on similar trips.",
  "summary": "Cold weather and an 18 mph headwind cost roughly 13% efficiency; pre-conditioning recovers most of the heating loss."
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

	// Regen recovery from physics. Anchors a "city stop-and-go"
	// or "long descent" factor with a concrete observation rather
	// than the model inferring it from speed and elevation alone.
	if pa := AnalyzeDrivePower(in.Samples, d); pa.DrawKwh > 0.1 {
		fmt.Fprintf(&b, "Regen recovered: %.2f kWh (%.0f%% of consumption)\n",
			pa.RegenKwh, pa.RegenPct)
	}

	// Speed distribution. Differentiates "highway slog" from "city
	// stop-and-go" — two fundamentally different efficiency stories
	// the previous prompt hid behind a single Avg/Max line.
	if buckets := SpeedBuckets(in.Samples); len(buckets) > 0 {
		var parts []string
		for _, sb := range buckets {
			if sb.Pct < 1 {
				// Drop noise rows so the line stays readable;
				// 0.4 % of a 35-min drive is 8 s of telemetry.
				continue
			}
			parts = append(parts, fmt.Sprintf("%s mph %.0f%%", sb.Label, sb.Pct))
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "Time by speed band: %s\n", strings.Join(parts, ", "))
		}
	}

	// Stops + low-speed time. City vs highway shows up here
	// independently of the speed distribution above; long highway
	// trips have zero stops while short urban hops hit double
	// digits. We label the duration "time below 5 mph" rather than
	// "idle time" — EVs don't idle the way ICE cars do (zero
	// powertrain draw when stationary), so the fleet-telemetry term
	// would be slightly misleading both to the LLM and to anyone
	// reading the prompt.
	if st := Stops(in.Samples); st.HasSignal {
		lowMin := st.IdleSec / 60
		fmt.Fprintf(&b, "Stops: %d (time below 5 mph %.1f min)\n", st.Count, lowMin)
	}

	// Drive mode share by time, not just the set of modes seen.
	// Sport for 5 min of a 60-min trip is barely a factor; Sport for
	// 45 min is the dominant story.
	if shares := DriveModeShares(in.Samples); len(shares) > 0 {
		var parts []string
		for _, dm := range shares {
			if dm.Pct < 5 {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %.0f%%", dm.Mode, dm.Pct))
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "Drive mode share: %s\n", strings.Join(parts, ", "))
		}
	}

	// Elevation (reuse helper from recap.go)
	if elev := summarizeElevation(in.Samples); elev.has {
		fmt.Fprintf(&b, "Elevation: %s (climb %d ft, descent %d ft, net %+d ft)\n",
			elev.profileLabel(), int(math.Round(elev.climbFt)), int(math.Round(elev.descentFt)), int(math.Round(elev.netFt)))
	}

	// Outside + cabin temperature. Cabin alone tells the model
	// little; the delta against outside is what reveals HVAC load.
	// We also keep the outside line for backward-compat with prior
	// prompts the model has seen in its training data.
	if t := medianNonZeroOutsideTempC(in.Samples); t != nil {
		if in.UseFahrenheit {
			fmt.Fprintf(&b, "Outside temp (median): %.0f F\n", *t*1.8+32)
		} else {
			fmt.Fprintf(&b, "Outside temp (median): %.0f C\n", *t)
		}
	}
	if ct, ok := CabinTemp(in.Samples); ok {
		if in.UseFahrenheit {
			fmt.Fprintf(&b, "Cabin temp (median): %.0f F", ct.MedianCabinC*1.8+32)
		} else {
			fmt.Fprintf(&b, "Cabin temp (median): %.0f C", ct.MedianCabinC)
		}
		if ct.HasDelta {
			// Multiply by 1.8 (not 1.8+32) for the delta —
			// a difference doesn't carry the freezing-point
			// offset, only the slope.
			if in.UseFahrenheit {
				fmt.Fprintf(&b, " (Δ %+.0f F vs outside)\n", ct.MedianDeltaC*1.8)
			} else {
				fmt.Fprintf(&b, " (Δ %+.0f C vs outside)\n", ct.MedianDeltaC)
			}
		} else {
			b.WriteString("\n")
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
	//
	// Two enrichments when we have the data:
	//   1. Placard delta. When the vehicle profile carries a
	//      TirePlacardPSI (door-jamb cold-fill), we cite the gap
	//      explicitly so the model attributes "Low tire pressure"
	//      against ground truth instead of guessing the placard
	//      from generic priors (which gets R1S 22" wrong).
	//   2. Temperature compensation note. Tire pressure drops
	//      ~1 psi per 10 F below the placard fill temp. Without
	//      the note, every cold-weather drive trips a "Low tire
	//      pressure" factor when nothing is actually wrong.
	if psi := medianTirePressurePSI(in.Samples); psi > 0 {
		var bits []string
		bits = append(bits, fmt.Sprintf("%.0f psi (median min corner)", psi))
		if vp := in.VehicleProfile; vp != nil && vp.TirePlacardPSI > 0 {
			delta := psi - vp.TirePlacardPSI
			bits = append(bits, fmt.Sprintf(
				"placard %.0f psi, %+.0f psi vs placard",
				vp.TirePlacardPSI, delta,
			))
		}
		if t := medianNonZeroOutsideTempC(in.Samples); t != nil {
			// Always include the temp-comp reminder when we
			// have an outside-temp value, regardless of
			// placard. Saves the model from over-attributing
			// underinflation on cold drives.
			bits = append(bits, "tire pressure drops ~1 psi per 10 F below the placard fill temp")
		}
		fmt.Fprintf(&b, "Tire pressure: %s\n", strings.Join(bits, "; "))
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
		trip = append(trip, "towing this trip (detected from drive mode)")
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
