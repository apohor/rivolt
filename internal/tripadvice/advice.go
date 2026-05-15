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

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/weather"
)

// Context bundles user-supplied labels and request parameters that
// would otherwise be unavailable in the plan response itself.
type Context struct {
	OriginLabel      string
	DestinationLabel string
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
	// DCFCNetworks is the user's edited price book from Settings.
	// Pass nil or empty to use the built-in defaults from dcfcrates.
	// Per-stop rates honour each row's MemberActive flag so the
	// estimate's user-totals match what the user has actually
	// signed up for.
	DCFCNetworks []dcfcrates.NetworkOverride
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

// CostEstimate is a deterministic, code-computed projection of trip
// cost. Four views the user cares about:
//   - DCFCSpend (guest): what a walk-up driver pays.
//   - DCFCSpendUserMember: cost given the user's active memberships
//     (toggled in Settings). Equal to DCFCSpend when the user has
//     no memberships on.
//   - DCFCSpendAllMember: hypothetical "you have every plan" floor,
//     so the strip can hint "you could save $X more by joining
//     Tesla Supercharging Membership".
//   - HomeEquivalent: every kWh at the home meter rate.
//
// Breakdown is the per-stop attribution that drives the totals, so
// the SPA can show "1× EA · 1× Tesla SC · 1× RAN".
type CostEstimate struct {
	Currency            string  `json:"currency"`
	DCFCSpend           float64 `json:"dcfc_spend"`
	DCFCSpendUserMember float64 `json:"dcfc_spend_user_member"`
	DCFCSpendAllMember  float64 `json:"dcfc_spend_all_member"`
	HomeEquivalent      float64 `json:"home_equivalent"`
	// DCFCRateUsed is the avg per-kWh guest rate weighted by energy.
	// Falls back to DefaultDCFCRateUSD when no waypoints priced.
	DCFCRateUsed           float64 `json:"dcfc_rate_used"`
	DCFCRateUsedUserMember float64 `json:"dcfc_rate_used_user_member"`
	DCFCRateUsedAllMember  float64 `json:"dcfc_rate_used_all_member"`
	// HomeRateUsed is the user's configured at-home rate, 0 when
	// unconfigured.
	HomeRateUsed float64 `json:"home_rate_used"`
	// Breakdown lists each priced stop. Empty when no DCFC stops
	// were on the route (all-from-home trip).
	Breakdown []CostStop `json:"breakdown,omitempty"`
}

// CostStop is one waypoint's contribution to the DCFC total.
type CostStop struct {
	NetworkSlug   string  `json:"network_slug"`
	NetworkName   string  `json:"network_name"`
	ChargerName   string  `json:"charger_name"`
	EnergyKWh     float64 `json:"energy_kwh"`
	GuestRate     float64 `json:"guest_rate"`
	UserRate      float64 `json:"user_rate"`
	AllMemberRate float64 `json:"all_member_rate"`
	MemberPlan    string  `json:"member_plan,omitempty"`
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
// first route in the plan. Done in Go (not the LLM) so the dollar
// figures stay accurate. Each charger stop's rate comes from the
// Networks table, matching on the planner's charger name, so the
// per-stop quote tracks the operator instead of a flat average.
func estimateCost(plan *rivian.TripPlan, tc Context) CostEstimate {
	cur := tc.HomeCurrency
	if cur == "" {
		cur = "USD"
	}
	est := CostEstimate{
		Currency:     cur,
		HomeRateUsed: tc.HomePricePerKWh,
	}
	if len(plan.Routes) == 0 {
		return est
	}
	r := plan.Routes[0]
	// Falls back to 0 when pack capacity is unknown - we don't want
	// to bake in a guess pack size and quote a dollar figure off it.
	if tc.PackKWh > 0 {
		var totalKWh float64
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
			rr := dcfcrates.ResolveRate(w.Name, tc.DCFCNetworks)
			est.DCFCSpend += energy * rr.GuestRate
			est.DCFCSpendUserMember += energy * rr.UserRate
			est.DCFCSpendAllMember += energy * rr.AllMemberRate
			est.Breakdown = append(est.Breakdown, CostStop{
				NetworkSlug:   rr.Slug,
				NetworkName:   rr.DisplayName,
				ChargerName:   w.Name,
				EnergyKWh:     energy,
				GuestRate:     rr.GuestRate,
				UserRate:      rr.UserRate,
				AllMemberRate: rr.AllMemberRate,
				MemberPlan:    rr.MemberPlan,
			})
			totalKWh += energy
		}
		// Energy-weighted averages for the strip's "@ $X / $Y / $Z per kWh" line.
		if totalKWh > 0 {
			est.DCFCRateUsed = est.DCFCSpend / totalKWh
			est.DCFCRateUsedUserMember = est.DCFCSpendUserMember / totalKWh
			est.DCFCRateUsedAllMember = est.DCFCSpendAllMember / totalKWh
		} else {
			est.DCFCRateUsed = DefaultDCFCRateUSD
			est.DCFCRateUsedUserMember = DefaultDCFCRateUSD
			est.DCFCRateUsedAllMember = DefaultDCFCRateUSD
		}
	}
	if tc.HomePricePerKWh > 0 && r.EnergyConsumptionKWh > 0 {
		est.HomeEquivalent = r.EnergyConsumptionKWh * tc.HomePricePerKWh
	}
	return est
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
	fmt.Fprintf(&sb, "Origin: %s\n", labelOrUnknown(tc.OriginLabel))
	fmt.Fprintf(&sb, "Destination: %s\n", labelOrUnknown(tc.DestinationLabel))
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
	if cost.DCFCSpend > 0 || cost.HomeEquivalent > 0 {
		fmt.Fprintf(&sb, "DCFC spend estimate: %.2f %s (assuming %.2f %s/kWh)\n",
			cost.DCFCSpend, cost.Currency, cost.DCFCRateUsed, cost.Currency)
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

func stopLabel(w rivian.PlannedWaypoint) string {
	if w.Name != "" {
		return w.Name
	}
	return fmt.Sprintf("(%.3f, %.3f)", w.Latitude, w.Longitude)
}

func labelOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
