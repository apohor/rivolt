// Package recap turns a finished drive into a short natural-language
// narration ("Trip recap") via the operator-configured LLM provider.
//
// Why this lives in its own package and not under internal/ai:
//   - internal/ai is provider-agnostic plumbing (Provider interface,
//     OpenAI/Anthropic/Gemini adapters, token cost table). Domain
//     features sit on top.
//   - The aggregator is pure: it takes a Drive + samples + a few
//     adjacent charges and produces a compact textual summary the
//     LLM operates on. No GPS coordinates, no per-second telemetry
//     ever leaves the box -- only summary statistics. This matters
//     for the BYO-key trust posture: even an OpenAI-keyed install
//     ships only the same scalars an analytics dashboard would.
//
// Cache strategy:
//   - drive_recaps (user_id, drive_id) PK -- migration 0020.
//   - Finished drives are immutable; a cached recap stays valid
//     forever. Regeneration is a user-initiated action.
//   - The handler is the cache reader/writer; this package only
//     knows about generation.
package recap

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// Result is what the generator returns to the handler.
type Result struct {
	Recap        string
	Model        string
	InputTokens  int64
	OutputTokens int64
}

// Inputs bundles every piece of context the prompt builder needs.
// Keeping this struct shallow makes the prompt unit-testable: feed
// it deterministic values, snapshot the resulting prompt string.
type Inputs struct {
	Drive   drives.Drive
	Samples []samples.Sample
	// AdjacentCharges are charge sessions that started or ended within
	// ~30 min of the drive window. The prompt mentions them so the
	// recap can say "charged at Buc-ee's afterwards" without us
	// having to reverse-geocode anything client-side.
	AdjacentCharges []charges.Charge
	// UseFahrenheit toggles the temperature unit shown in the prompt.
	// Distance is always miles + feet because the app surface is
	// imperial everywhere; revisit if a metric pref ever lands.
	UseFahrenheit bool
}

// Generate calls the LLM and returns a 2-3 sentence recap. Caller
// (handler) owns the cache. The analyzer must be non-nil; the
// handler 503s before reaching here when AI is unconfigured.
func Generate(ctx context.Context, a *ai.Analyzer, in Inputs) (Result, error) {
	if a == nil {
		return Result{}, fmt.Errorf("recap: analyzer is nil")
	}
	system, user := buildPrompt(in)
	reply, usage, err := a.Complete(ctx, system, user)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Recap:        strings.TrimSpace(reply),
		Model:        a.ModelName(),
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}, nil
}

// buildPrompt is the deterministic, side-effect-free core of the
// generator. Returns (system, user) so callers can inspect/test it
// without touching the network.
//
// The system prompt pins voice and length; the user prompt is a
// compact YAML-like block of stats. We do NOT send raw samples or
// GPS coordinates -- only aggregates and a coarse elevation profile
// (8 bucket means). That's enough for the model to write a credible
// narration while keeping per-second telemetry on the operator's
// disk.
func buildPrompt(in Inputs) (string, string) {
	const system = `You are the Rivolt trip-recap writer. ` +
		`Produce 2-3 short sentences (max ~60 words total) summarizing ` +
		`a single Rivian drive in a friendly, factual tone -- like a ` +
		`car-enthusiast friend describing the trip. Lead with the ` +
		`headline numbers (distance, efficiency, cost if known). ` +
		`Mention notable details only when they matter: significant ` +
		`elevation gain/loss, unusually high or low efficiency, an ` +
		`adjacent charge stop, weather extremes. Do not invent ` +
		`information. Do not include emoji, hashtags, or marketing ` +
		`language. Output the recap text only -- no preamble, no ` +
		`bullet points, no markdown.`

	d := in.Drive
	dur := d.EndedAt.Sub(d.StartedAt)

	// Energy efficiency in mi/kWh; guard against the legacy
	// EnergyUsedKWh==0 case (pre-migration-0002 imports) so the
	// model isn't told the car got infinity miles to the kWh.
	var miPerKWh float64
	if d.EnergyUsedKWh > 0.05 && d.DistanceMi > 0 {
		miPerKWh = d.DistanceMi / d.EnergyUsedKWh
	}

	// Elevation profile from per-sample altitude_m. We feed the model
	// (a) net climb/descent in feet and (b) eight evenly-spaced bucket
	// means so it can describe shape ("rolling", "uphill the whole
	// way") without needing the raw trace. Skips entirely when no
	// sample carries altitude (legacy / imported drives).
	elev := summarizeElevation(in.Samples)

	// Outside temperature: pick the single representative reading
	// (median of non-zero outside-temp samples). The 0 sentinel from
	// internal/rivian/live.go means "Rivian's WS feed didn't carry
	// outside temp" -- we filter those out so the model isn't told
	// the trip happened at 0 C.
	outsideC := medianNonZeroOutsideTempC(in.Samples)

	// Cost: pull from the first non-empty FinalEnergyCostUSD on a
	// charge that landed within the drive window or immediately
	// after. Drives don't have their own cost field; the operator
	// pays per kWh, so the cost lives on the adjacent charge.
	var sb strings.Builder
	fmt.Fprintln(&sb, "Drive stats:")
	fmt.Fprintf(&sb, "  date: %s\n", d.StartedAt.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&sb, "  distance_mi: %.1f\n", d.DistanceMi)
	fmt.Fprintf(&sb, "  duration: %s\n", roundDuration(dur))
	if d.AvgSpeedMph > 0 {
		fmt.Fprintf(&sb, "  avg_speed_mph: %.0f\n", d.AvgSpeedMph)
	}
	if d.MaxSpeedMph > 0 {
		fmt.Fprintf(&sb, "  max_speed_mph: %.0f\n", d.MaxSpeedMph)
	}
	if d.EnergyUsedKWh > 0.05 {
		fmt.Fprintf(&sb, "  energy_used_kwh: %.1f\n", d.EnergyUsedKWh)
	}
	if miPerKWh > 0 {
		fmt.Fprintf(&sb, "  efficiency_mi_per_kwh: %.2f\n", miPerKWh)
	}
	fmt.Fprintf(&sb, "  start_soc_pct: %.0f\n", d.StartSoCPct)
	fmt.Fprintf(&sb, "  end_soc_pct: %.0f\n", d.EndSoCPct)

	if elev.has {
		fmt.Fprintf(&sb, "  net_elevation_change_ft: %+.0f\n", elev.netFt)
		fmt.Fprintf(&sb, "  total_climb_ft: %.0f\n", elev.climbFt)
		fmt.Fprintf(&sb, "  total_descent_ft: %.0f\n", elev.descentFt)
		// Profile as 8 bucket means relative to the start, in feet.
		fmt.Fprintf(&sb, "  elevation_profile_ft_relative: [%s]\n", elev.profileLabel())
	}

	if outsideC != nil {
		if in.UseFahrenheit {
			fmt.Fprintf(&sb, "  outside_temp_f: %.0f\n", *outsideC*1.8+32)
		} else {
			fmt.Fprintf(&sb, "  outside_temp_c: %.0f\n", *outsideC)
		}
	}

	if len(in.AdjacentCharges) > 0 {
		fmt.Fprintln(&sb, "Adjacent charges:")
		for _, c := range in.AdjacentCharges {
			fmt.Fprintf(&sb, "  - when: %s, energy_added_kwh: %.1f, peak_kw: %.0f, soc: %.0f-%.0f%%, cost: %.2f %s\n",
				chargeWhen(c, d),
				c.EnergyAddedKWh, c.MaxPowerKW,
				c.StartSoCPct, c.EndSoCPct,
				c.Cost, currencyOrUSD(c))
		}
	}

	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Write the recap now.")
	return system, sb.String()
}

// chargeWhen classifies a charge relative to the drive window so
// the prompt can read "before"/"during"/"after" instead of forcing
// the model to do timestamp math.
func chargeWhen(c charges.Charge, d drives.Drive) string {
	if !c.EndedAt.IsZero() && c.EndedAt.Before(d.StartedAt) {
		return "before"
	}
	if !c.StartedAt.IsZero() && c.StartedAt.After(d.EndedAt) {
		return "after"
	}
	return "during"
}

// currencyOrUSD falls back to USD when the charge row didn't carry
// a currency code (live recorder + ElectraFi imports both default
// to USD). Keeps the prompt's units explicit.
func currencyOrUSD(c charges.Charge) string {
	if c.Currency == "" {
		return "USD"
	}
	return c.Currency
}

// elevationSummary holds the prompt-ready elevation aggregates.
type elevationSummary struct {
	has                bool
	netFt              float64
	climbFt, descentFt float64
	// profile holds 8 bucket means in feet, expressed relative to
	// the first bucket (so the first value is always 0 and the model
	// reads "shape", not "absolute meters above sea level"). nil when
	// no samples carry altitude.
	profile []float64
}

func (s elevationSummary) profileLabel() string {
	parts := make([]string, len(s.profile))
	for i, v := range s.profile {
		parts[i] = fmt.Sprintf("%+.0f", v)
	}
	return strings.Join(parts, ", ")
}

// summarizeElevation computes net + total climb/descent + an 8-bucket
// profile from per-sample altitude. Light filter: ignore quantisation
// noise smaller than 1 m between adjacent samples (Terrarium's int16
// encoding rounds to ~1 m). Returns has=false when fewer than 2
// samples carry altitude_m.
func summarizeElevation(ss []samples.Sample) elevationSummary {
	pts := make([]float64, 0, len(ss))
	for _, s := range ss {
		if s.AltitudeM != nil {
			pts = append(pts, *s.AltitudeM)
		}
	}
	if len(pts) < 2 {
		return elevationSummary{}
	}
	const mToFt = 3.28084
	var climb, descent float64
	for i := 1; i < len(pts); i++ {
		delta := pts[i] - pts[i-1]
		if math.Abs(delta) < 1.0 { // ignore DEM int16 jitter
			continue
		}
		if delta > 0 {
			climb += delta
		} else {
			descent += -delta
		}
	}
	net := pts[len(pts)-1] - pts[0]

	// 8-bucket means, relative to the start.
	const buckets = 8
	profile := make([]float64, buckets)
	step := float64(len(pts)) / float64(buckets)
	startMean := bucketMean(pts, 0, int(math.Round(step)))
	for b := 0; b < buckets; b++ {
		lo := int(math.Round(float64(b) * step))
		hi := int(math.Round(float64(b+1) * step))
		if hi > len(pts) {
			hi = len(pts)
		}
		profile[b] = (bucketMean(pts, lo, hi) - startMean) * mToFt
	}

	return elevationSummary{
		has:       true,
		netFt:     net * mToFt,
		climbFt:   climb * mToFt,
		descentFt: descent * mToFt,
		profile:   profile,
	}
}

func bucketMean(pts []float64, lo, hi int) float64 {
	if hi <= lo || lo >= len(pts) {
		return 0
	}
	if hi > len(pts) {
		hi = len(pts)
	}
	var sum float64
	for _, v := range pts[lo:hi] {
		sum += v
	}
	return sum / float64(hi-lo)
}

// medianNonZeroOutsideTempC returns the median of non-sentinel
// outside temperature readings, or nil if the drive carries none.
// The (0, 0) sentinel is filtered out for the same reason the chart
// path filters it: Rivian's live WS feed reports 0 °C when the data
// isn't carried, and a phantom 0 line would distort the prompt.
func medianNonZeroOutsideTempC(ss []samples.Sample) *float64 {
	xs := make([]float64, 0, len(ss))
	for _, s := range ss {
		if s.OutsideTempC != 0 {
			xs = append(xs, s.OutsideTempC)
		}
	}
	if len(xs) == 0 {
		return nil
	}
	// Median via partial sort.
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
	med := xs[len(xs)/2]
	return &med
}

// roundDuration drops sub-second precision so the prompt reads
// "1h 12m" instead of "1h12m3.4s". time.Duration's String is fine
// once truncated to the minute.
func roundDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
