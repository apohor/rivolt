// Package tripadvice wraps the operator-configured AI provider to
// generate plain-language observations about a Rivian trip plan:
// charging stop summary, arrival SoC commentary, adapter warnings,
// and drive-mode suggestions when the plan looks sub-optimal.
//
// Structure mirrors internal/recap: side-effect-free buildPrompt,
// stateless Generate, structured JSON output the SPA renders as a
// card below the route table.
package tripadvice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/weather"
)

// Context bundles user-supplied labels and request parameters that
// would otherwise be unavailable in the plan response itself.
//
// Privacy: what the model receives about places is limited to charger names,
// and locations rounded to weather.CoarseRound (0.1 deg, ~11 km). The typed
// origin/destination text (which may be a street address) is deliberately
// not part of Context, and no full-precision coordinate is ever written
// into the prompt.
type Context struct {
	// DriveMode is the mode the user explicitly chose, or "" for
	// "let the planner pick". Included so the model can say "you
	// planned in Conserve mode" rather than treating it as a mystery.
	DriveMode   string
	StartingSoC float64
	HasAdapter  bool
	// Weather at the origin at departure time. nil when the operator
	// has not enabled the weather feature (recap.weather_enabled).
	Weather *weather.Snapshot
	// TirePressurebars holds [FL, FR, RL, RR] in bar from the live
	// vehicle state. Zero values mean unavailable for that corner.
	TirePressureBars [4]float64
	// PackKWh is the observed battery capacity in kWh from the vehicle
	// record. 0 means unknown.
	PackKWh float64
	// TirePlacardPSI is the user-configured door-jamb cold-fill
	// pressure from the vehicle profile. 0 means unconfigured; the
	// prompt then falls back to a generic "below placard" framing
	// instead of citing a specific number.
	TirePlacardPSI float64
	// HomePricePerKWh is the user's at-home charging cost in
	// HomeCurrency (typically USD). 0 means unconfigured — the cost
	// section degrades to a pure-DCFC estimate.
	HomePricePerKWh float64
	HomeCurrency    string
	// GasPricePerGallon × distance / ComparisonMPG drives the gas-
	// equivalent chip. Both must be > 0 for the chip to render.
	GasPricePerGallon float64
	ComparisonMPG     float64
	// DCFCNetworks is the user's edited price book from Settings.
	// Pass nil or empty to use the built-in defaults from dcfcrates.
	// Per-stop rates honour each row's MemberActive flag so the
	// estimate's user-totals match what the user has actually
	// signed up for.
	DCFCNetworks []dcfcrates.NetworkOverride
	// OwnRates is what the user actually paid per kWh at each network (slug), from
	// their own receipts, with how many sessions it rests on. The caller supplies
	// only figures that meet the minimum (receipts.PaidMinSessions) in this
	// context's currency. nil when the user has no receipts.
	OwnRates map[string]OwnRate
	// Now is the clock for the observed-price expiry; zero means time.Now().
	Now time.Time
}

// OwnRate is the user's own paid price at a network.
type OwnRate struct {
	PerKWh   float64
	Sessions int
}

// Result is what Generate returns to the handler.
type Result struct {
	// Raw is the model's reply, stored and forwarded as-is.
	Raw          string
	Parsed       *Parsed
	Model        string
	InputTokens  int64
	OutputTokens int64
	// Cost is computed deterministically in Go (not by the LLM) so
	// the dollar figure is always accurate. The LLM gets the same
	// numbers in the prompt and writes the commentary around them.
	Cost CostEstimate
}

// CostEstimate is a deterministic, code-computed projection of trip cost. It is an
// ESTIMATE and says so: the dollar figures for the DC fast stops are a range
// (DCFCSpendLow to DCFCSpendHigh) over the stops that have a price, and a stop with
// none is counted in UnpricedStops, not given a made-up number. Where each stop's
// price comes from is on the stop (CostStop.Price.Basis), in order:
//
//  1. the user's own receipts at that network (their paid $/kWh, at least 3 sessions);
//  2. a rate the user set themselves in Settings (not the seeded default);
//  3. the third-party observed price (dcfcrates.ObservedFor: its own table, source and
//     month, hidden after 12 months);
//  4. none: "price shown at the charger".
//
// The built-in table's dated guest and member rates are never used for a dollar
// figure. HomeEquivalent and GasEquivalent are unchanged.
type CostEstimate struct {
	Currency string `json:"currency"`
	// DCFCSpendLow and DCFCSpendHigh sum the priced stops' cost range. Equal when no
	// priced stop has a range; both 0 when no stop has a price (see PricedStops).
	DCFCSpendLow  float64 `json:"dcfc_spend_low"`
	DCFCSpendHigh float64 `json:"dcfc_spend_high"`
	// PricedStops and UnpricedStops count the DC fast stops with and without a price,
	// so the UI can say "partial: 2 of 3 stops priced".
	PricedStops   int `json:"priced_stops"`
	UnpricedStops int `json:"unpriced_stops"`
	// EstimateBasis is which sources fed the figure: "your_receipts", "your_setting",
	// "observed", "mixed" (more than one of them) or "none" (no stop priced). The UI
	// words the estimate from it ("estimate (typical observed price)" when observed or
	// mixed).
	EstimateBasis  string  `json:"estimate_basis"`
	HomeEquivalent float64 `json:"home_equivalent"`
	// GasEquivalent is what the same distance would cost in an ICE
	// vehicle at the user's configured gas price + MPG. Zero when
	// either input is unset.
	GasEquivalent     float64 `json:"gas_equivalent"`
	GasPricePerGallon float64 `json:"gas_price_per_gallon"`
	GasMPG            float64 `json:"gas_mpg"`
	// HomeRateUsed is the user's configured at-home rate, 0 when
	// unconfigured.
	HomeRateUsed float64 `json:"home_rate_used"`
	// Breakdown lists each DC fast stop. Empty when no DCFC stops
	// were on the route (all-from-home trip).
	Breakdown []CostStop `json:"breakdown,omitempty"`
}

// Price bases for StopPrice.Basis.
const (
	BasisYourReceipts = "your_receipts"
	BasisYourSetting  = "your_setting"
	BasisObserved     = "observed"
)

// StopPrice is the price used for one stop and where it came from.
type StopPrice struct {
	Basis      string  `json:"basis"`
	PerKWhLow  float64 `json:"per_kwh_low"`
	PerKWhHigh float64 `json:"per_kwh_high"`
	// LowNote and HighNote say what each end of a range is (observed Tesla: with and
	// without the membership); empty when the ends are equal.
	LowNote  string `json:"low_note,omitempty"`
	HighNote string `json:"high_note,omitempty"`
	// Sessions is how many of the user's receipts a your_receipts figure rests on.
	Sessions int `json:"sessions,omitempty"`
	// Source, SourceURL and ObservedMonth name the third party behind an observed figure.
	Source        string `json:"source,omitempty"`
	SourceURL     string `json:"source_url,omitempty"`
	ObservedMonth string `json:"observed_month,omitempty"`
	// Label is the exact wording to show with the figure.
	Label string `json:"label"`
}

// CostStop is one waypoint's contribution to the DC fast total.
type CostStop struct {
	NetworkSlug string  `json:"network_slug"`
	NetworkName string  `json:"network_name"`
	ChargerName string  `json:"charger_name"`
	EnergyKWh   float64 `json:"energy_kwh"`
	MemberPlan  string  `json:"member_plan,omitempty"`
	// Price is nil when there is none for this stop: "price shown at the charger".
	Price *StopPrice `json:"price"`
	// CostLow and CostHigh are EnergyKWh times the price range, nil with no price.
	CostLow  *float64 `json:"cost_low"`
	CostHigh *float64 `json:"cost_high"`
}

// Parsed is the structured JSON shape the model emits. Each list
// holds 1-3 short sentences; sections with nothing useful to say
// stay empty rather than emitting filler.
type Parsed struct {
	Headline   string   `json:"headline"`
	Cost       []string `json:"cost"`
	Efficiency []string `json:"efficiency"`
	Weather    []string `json:"weather"`
	Vehicle    []string `json:"vehicle"`
}

// DefaultDCFCRateUSD is the per-kWh fallback used in the rare case
// where the cost estimator runs without a route (pre-network-table
// API consumers, tests). The real per-stop pricing flows through
// the Networks table in networks.go.
const DefaultDCFCRateUSD = 0.46

// Generate calls the AI provider with a compact trip-plan summary
// and returns structured advice. The context's deadline should be
// generous (30s+) — sit in the AI-bound route group, not the timed
// one.
func Generate(ctx context.Context, a *ai.Analyzer, plan *rivian.TripPlan, tc Context) (Result, error) {
	cost := estimateCost(plan, tc)
	system, user := buildPrompt(plan, tc, cost)
	raw, usage, err := a.Complete(ctx, system, user)
	if err != nil {
		return Result{}, err
	}
	var res Result
	res.Raw = raw
	res.Model = a.ModelName()
	res.InputTokens = usage.InputTokens
	res.OutputTokens = usage.OutputTokens
	res.Parsed = parse(raw)
	res.Cost = cost
	return res, nil
}

// estimateCost projects DCFC spend + home-rate-equivalent for the
// first route in the plan. Kept as a thin shim around EstimateRoute
// so the AI advice prompt - which only cares about the recommended
// route - doesn't have to change. The plan response uses
// EstimateRoute directly to attach a cost to every route.
func estimateCost(plan *rivian.TripPlan, tc Context) CostEstimate {
	if len(plan.Routes) == 0 {
		cur := tc.HomeCurrency
		if cur == "" {
			cur = "USD"
		}
		return CostEstimate{Currency: cur, HomeRateUsed: tc.HomePricePerKWh}
	}
	return EstimateRoute(&plan.Routes[0], tc)
}

// EstimateRoute projects DCFC spend + home-rate-equivalent for a
// single route. Exported so the /api/trips/plan handler can attach a
// cost to every route in the response without making the SPA round-
// trip through the AI advice endpoint just to see numbers.
func EstimateRoute(r *rivian.TripRoute, tc Context) CostEstimate {
	cur := tc.HomeCurrency
	if cur == "" {
		cur = "USD"
	}
	est := CostEstimate{
		Currency:     cur,
		HomeRateUsed: tc.HomePricePerKWh,
	}
	if r == nil {
		return est
	}
	// Falls back to 0 when pack capacity is unknown - we don't want
	// to bake in a guess pack size and quote a dollar figure off it.
	if tc.PackKWh > 0 {
		now := tc.Now
		if now.IsZero() {
			now = time.Now()
		}
		bases := map[string]bool{}
		for _, w := range r.Waypoints {
			t := strings.ToUpper(w.WaypointType)
			if t == "ORIGIN" || t == "DESTINATION" || t == "WAYPOINT" || t == "OTHER" {
				continue
			}
			delta := w.DepartureSoC - w.ArrivalSoC
			if delta <= 0 {
				continue
			}
			energy := (delta / 100) * tc.PackKWh
			sn := dcfcrates.ResolveStop(w.Name, tc.DCFCNetworks)
			stop := CostStop{
				NetworkSlug: sn.Slug, NetworkName: sn.DisplayName, ChargerName: w.Name,
				EnergyKWh: energy, MemberPlan: sn.MemberPlan,
				Price: stopPrice(sn, tc, cur, now),
			}
			if p := stop.Price; p != nil {
				lo, hi := energy*p.PerKWhLow, energy*p.PerKWhHigh
				stop.CostLow, stop.CostHigh = &lo, &hi
				est.DCFCSpendLow += lo
				est.DCFCSpendHigh += hi
				est.PricedStops++
				bases[p.Basis] = true
			} else {
				est.UnpricedStops++
			}
			est.Breakdown = append(est.Breakdown, stop)
		}
		switch len(bases) {
		case 0:
			est.EstimateBasis = "none"
		case 1:
			for b := range bases {
				est.EstimateBasis = b
			}
		default:
			est.EstimateBasis = "mixed"
		}
	}
	if tc.HomePricePerKWh > 0 && r.EnergyConsumptionKWh > 0 {
		est.HomeEquivalent = r.EnergyConsumptionKWh * tc.HomePricePerKWh
	}
	if tc.GasPricePerGallon > 0 && tc.ComparisonMPG > 0 && r.TotalDriveDistanceMeters > 0 {
		miles := r.TotalDriveDistanceMeters / 1609.344
		est.GasEquivalent = miles / tc.ComparisonMPG * tc.GasPricePerGallon
		est.GasPricePerGallon = tc.GasPricePerGallon
		est.GasMPG = tc.ComparisonMPG
	}
	return est
}

// stopPrice picks the price for a stop, in order: the user's own receipts, a rate the
// user set, the observed third-party figure, none. Observed figures are USD, so they
// are skipped when the trip is in another currency.
func stopPrice(sn dcfcrates.StopNetwork, tc Context, cur string, now time.Time) *StopPrice {
	if o, ok := tc.OwnRates[sn.Slug]; ok && o.PerKWh > 0 && o.Sessions > 0 {
		return &StopPrice{
			Basis: BasisYourReceipts, PerKWhLow: o.PerKWh, PerKWhHigh: o.PerKWh, Sessions: o.Sessions,
			Label: fmt.Sprintf("Your average over your last %d sessions", o.Sessions),
		}
	}
	if sn.UserSet && sn.UserPerKWh > 0 {
		return &StopPrice{
			Basis: BasisYourSetting, PerKWhLow: sn.UserPerKWh, PerKWhHigh: sn.UserPerKWh,
			Label: "Your saved rate",
		}
	}
	if cur == "USD" {
		if o, ok := dcfcrates.ObservedFor(sn.Slug, now); ok {
			p := &StopPrice{
				Basis: BasisObserved, PerKWhLow: o.Low, PerKWhHigh: o.High,
				Source: dcfcrates.ObservedSource, SourceURL: o.URL, ObservedMonth: o.Month, Label: o.Label(),
			}
			if o.Low != o.High {
				p.LowNote, p.HighNote = o.LowNote, o.HighNote
			}
			return p
		}
	}
	return nil
}

func parse(s string) *Parsed {
	s = strings.TrimSpace(s)
	// Strip optional markdown code fence.
	if after, ok := strings.CutPrefix(s, "```json"); ok {
		s = strings.TrimSuffix(strings.TrimSpace(after), "```")
	} else if after, ok := strings.CutPrefix(s, "```"); ok {
		s = strings.TrimSuffix(strings.TrimSpace(after), "```")
	}
	var p Parsed
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil
	}
	return &p
}

func buildPrompt(plan *rivian.TripPlan, tc Context, cost CostEstimate) (system, user string) {
	const sys = `You are a concise Rivian trip-planning assistant. ` +
		`You receive a summary of a trip plan generated by Rivian's own planner and the user's starting parameters. ` +
		`Return a single JSON object — nothing before or after it — with this exact shape:
` +
		"```\n" +
		`{"headline":"<8 words or fewer>","cost":["<sentence>"],"efficiency":["<sentence>"],"weather":["<sentence>"],"vehicle":["<sentence>"]}` + "\n```\n" +
		`Rules:
- Each section holds 0–3 short sentences. Skip a section entirely (empty list) when you have nothing useful to add for it; do NOT pad with filler.
- "headline" is 8 words or fewer summarising the trip vibe.
- "cost": comment on the DCFC spend and home-equivalent already computed below. Mention if charging at fewer/different stops would save money; if the trip uses zero DCFC (all from battery stored at home), say so plainly. Don't restate the numbers, frame them.
- "efficiency": 1–3 actionable tips. Drive mode (e.g. Conserve saves time on long trips, Sport burns range), departure timing (avoid peak heat / cold), trailer/load if relevant.
- "weather": only when there's something material — headwind > 15 kph, temp < 0 °C or > 35 °C, precipitation, thunderstorm forecast. Quantify the range impact when you can.
- "vehicle": tire pressures vs. the placard PSI (when provided below), pack capacity vs. trip distance, adapter dependency at any stop. If the placard is missing, frame tire commentary generically ("below the door-jamb spec") rather than citing a specific number.
- Lead each section with the most actionable observation. Do not invent numbers. No emoji, no marketing language.`

	var sb strings.Builder
	fmt.Fprintf(&sb, "Origin: %s\n", endpointLabel(plan, "ORIGIN"))
	fmt.Fprintf(&sb, "Destination: %s\n", endpointLabel(plan, "DESTINATION"))
	fmt.Fprintf(&sb, "Starting SoC: %.0f%%\n", tc.StartingSoC)
	if tc.DriveMode != "" {
		fmt.Fprintf(&sb, "Drive mode: %s\n", tc.DriveMode)
	} else {
		fmt.Fprintf(&sb, "Drive mode: default (planner chose)\n")
	}
	if tc.HasAdapter {
		fmt.Fprintf(&sb, "Tesla NACS adapter: yes\n")
	}

	if tc.PackKWh > 0 {
		fmt.Fprintf(&sb, "Battery pack: %.0f kWh\n", tc.PackKWh)
	}

	// Tire pressures: convert bar → PSI, report only corners with data.
	const barToPSI = 14.5038
	tireLabels := [4]string{"FL", "FR", "RL", "RR"}
	var tireLines []string
	for i, bar := range tc.TirePressureBars {
		if bar > 0 {
			tireLines = append(tireLines, fmt.Sprintf("%s %.0f PSI", tireLabels[i], bar*barToPSI))
		}
	}
	if len(tireLines) > 0 {
		fmt.Fprintf(&sb, "Tire pressures: %s\n", strings.Join(tireLines, ", "))
	}
	if tc.TirePlacardPSI > 0 {
		fmt.Fprintf(&sb, "Tire placard PSI: %.0f (door-jamb cold-fill)\n", tc.TirePlacardPSI)
	}

	if w := tc.Weather; w != nil {
		if w.HasTemp {
			fmt.Fprintf(&sb, "Outside temp: %.0f °C", w.TempC)
			if w.HasApparent {
				fmt.Fprintf(&sb, " (feels like %.0f °C)", w.ApparentTempC)
			}
			fmt.Fprintln(&sb)
		}
		if w.HasWind {
			fmt.Fprintf(&sb, "Wind: %.0f kph from %.0f°", w.WindKPH, w.WindDirDeg)
			if w.HasHeadwind {
				if w.HeadwindKPH > 0 {
					fmt.Fprintf(&sb, " (%.0f kph headwind)", w.HeadwindKPH)
				} else {
					fmt.Fprintf(&sb, " (%.0f kph tailwind)", -w.HeadwindKPH)
				}
			}
			fmt.Fprintln(&sb)
		}
		if w.HasConditions {
			fmt.Fprintf(&sb, "Conditions: %s\n", w.Conditions)
		}
		if w.HasPrecip && w.PrecipMM > 0 {
			fmt.Fprintf(&sb, "Precipitation: %.1f mm\n", w.PrecipMM)
		}
	}

	// Cost summary — code-computed, given to the model so the "cost"
	// commentary references real numbers instead of inventing them.
	if cost.PricedStops > 0 || cost.HomeEquivalent > 0 {
		if cost.PricedStops > 0 {
			spend := fmt.Sprintf("%.2f", cost.DCFCSpendLow)
			if cost.DCFCSpendHigh-cost.DCFCSpendLow >= 0.005 {
				spend = fmt.Sprintf("%.2f to %.2f", cost.DCFCSpendLow, cost.DCFCSpendHigh)
			}
			fmt.Fprintf(&sb, "DCFC spend estimate: %s %s for %d of %d fast-charging stops (basis: %s); the other stops have no known price, so say the price is shown at the charger\n",
				spend, cost.Currency, cost.PricedStops, cost.PricedStops+cost.UnpricedStops, cost.EstimateBasis)
		} else if cost.UnpricedStops > 0 {
			fmt.Fprintln(&sb, "DCFC spend: unknown, the price is shown at the charger. Do not quote a dollar figure for the fast-charging stops.")
		}
		if cost.HomeEquivalent > 0 {
			fmt.Fprintf(&sb, "Home-rate equivalent for total energy used: %.2f %s (at %.3f %s/kWh)\n",
				cost.HomeEquivalent, cost.Currency, cost.HomeRateUsed, cost.Currency)
		}
	} else if tc.PackKWh == 0 {
		fmt.Fprintln(&sb, "Cost estimate unavailable: pack capacity unknown.")
	}

	fmt.Fprintln(&sb)

	if len(plan.Routes) == 0 {
		fmt.Fprintf(&sb, "The planner returned no viable routes. Status: %s\n", plan.Status)
		if plan.SoCBelowLimit {
			fmt.Fprintln(&sb, "Rivian flagged SoC as below the configured limit.")
		}
		if !plan.ChargeStationsAvailable {
			fmt.Fprintln(&sb, "No charge stations available along the corridor.")
		}
		return sys, sb.String()
	}

	for i, r := range plan.Routes {
		charging := chargeStops(r)
		totalChargeMin := r.TotalChargingDurationSec / 60
		fmt.Fprintf(&sb, "Route %d:\n", i+1)
		fmt.Fprintf(&sb, "  Destination reached: %v\n", r.DestinationReached)
		fmt.Fprintf(&sb, "  Charging stops: %d\n", len(charging))
		if totalChargeMin > 0 {
			fmt.Fprintf(&sb, "  Total charge time: %d min\n", totalChargeMin)
		}
		if r.ArrivalSoC > 0 {
			fmt.Fprintf(&sb, "  Arrival SoC: %.0f%%\n", r.ArrivalSoC)
		}
		if r.EnergyConsumptionKWh > 0 {
			fmt.Fprintf(&sb, "  Energy used: %.1f kWh\n", r.EnergyConsumptionKWh)
		}
		if !r.DestinationReached && r.BatteryEmptyToDestMeters > 0 {
			fmt.Fprintf(&sb, "  Battery empty %.0f km short of destination\n", r.BatteryEmptyToDestMeters/1000)
		}
		for j, w := range charging {
			fmt.Fprintf(&sb, "  Stop %d: %s\n", j+1, stopLabel(w))
			fmt.Fprintf(&sb, "    Arrive %.0f%% → depart %.0f%% (%d min", w.ArrivalSoC, w.DepartureSoC, w.ChargeDurationSec/60)
			if w.MaxPowerKW > 0 {
				fmt.Fprintf(&sb, ", max %.0f kW", w.MaxPowerKW)
			}
			fmt.Fprintln(&sb, ")")
			if w.AdapterRequired {
				fmt.Fprintln(&sb, "    Adapter required at this stop.")
			}
		}
		if plan.SoCBelowLimit {
			fmt.Fprintln(&sb, "  Rivian flagged SoC below limit.")
		}
	}
	return sys, sb.String()
}

func chargeStops(r rivian.TripRoute) []rivian.PlannedWaypoint {
	var out []rivian.PlannedWaypoint
	for _, w := range r.Waypoints {
		t := strings.ToUpper(w.WaypointType)
		if t != "ORIGIN" && t != "DESTINATION" && t != "OTHER" {
			out = append(out, w)
		}
	}
	return out
}

// stopLabel names a charging stop: the charger's name when Rivian gave
// one, otherwise its location rounded to the weather grid.
func stopLabel(w rivian.PlannedWaypoint) string {
	if w.Name != "" {
		return w.Name
	}
	return coarsePlace(w.Latitude, w.Longitude)
}

// endpointLabel describes the trip's origin or destination by its location
// rounded to ~11 km, never by the typed text or the waypoint's name (either
// may be a street address). "unknown" when the plan has no such waypoint.
func endpointLabel(plan *rivian.TripPlan, waypointType string) string {
	for _, r := range plan.Routes {
		for _, w := range r.Waypoints {
			if strings.EqualFold(w.WaypointType, waypointType) {
				return coarsePlace(w.Latitude, w.Longitude)
			}
		}
	}
	return "unknown"
}

func coarsePlace(lat, lon float64) string {
	lat, lon = weather.Coarsen(lat, lon)
	return fmt.Sprintf("near %.1f, %.1f (rounded to ~11 km)", lat, lon)
}
