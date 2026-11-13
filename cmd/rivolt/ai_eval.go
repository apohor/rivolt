package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/recap"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/weather"
)

// aiEvalModels are the models compared, cheapest first.
var aiEvalModels = []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"}

// runAIEval runs the three AI routes (recap, efficiency, advice) on the three candidate models over built-in demo
// inputs and prints the answers side by side as Markdown, with tokens, cost and latency per call. It reads the key from
// ANTHROPIC_API_KEY, never prints it, touches no database and starts no server; it stops once the running cost reaches
// --budget (the call that crosses it still completes). Inputs are the demo account's shapes (no real user data).
func runAIEval(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("ai-eval", flag.ContinueOnError)
	fs.SetOutput(errOut)
	budget := fs.Float64("budget", 2.0, "stop once the total cost (USD) reaches this")
	perRoute := fs.Int("inputs", 5, "inputs per route")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		fmt.Fprintln(errOut, "ai-eval: ANTHROPIC_API_KEY is not set")
		return 1
	}
	cases, err := aiEvalCases(*perRoute)
	if err != nil {
		fmt.Fprintln(errOut, "ai-eval:", err)
		return 1
	}

	var served []string
	analyzers := map[string]*ai.Analyzer{}
	for _, m := range aiEvalModels {
		p, err := ai.NewAnthropic(ai.AnthropicConfig{
			APIKey: key, Model: m, Operator: true, ForceEffort: true,
			Observe: func(u ai.OperatorUsage) { served = append(served, u.Model) },
		})
		if err != nil {
			fmt.Fprintln(errOut, "ai-eval:", err)
			return 1
		}
		analyzers[m] = ai.NewAnalyzer(p)
	}

	var total float64
	fmt.Fprintf(out, "# AI model side-by-side\n\nBudget cap $%.2f. Every call uses the route's production effort and max_tokens, demo inputs only.\n", *budget)
	for _, c := range cases {
		fmt.Fprintf(out, "\n## %s: %s\n\n", c.route, c.name)
		for _, m := range aiEvalModels {
			if total >= *budget {
				fmt.Fprintf(out, "### %s\n\nSKIPPED: budget reached ($%.4f spent)\n\n", m, total)
				continue
			}
			ctx := ai.WithUnmetered(ai.WithRoute(context.Background(), c.route))
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			served = served[:0]
			start := time.Now()
			text, usage, err := c.call(ctx, analyzers[m])
			lat := time.Since(start)
			cancel()
			cost := servedCost(m, served, usage)
			total += cost
			fmt.Fprintf(out, "### %s\n\n", m)
			fmt.Fprintf(out, "in %d tok, out %d tok, $%.4f, %.1f s, served by %s\n\n", usage.InputTokens, usage.OutputTokens, cost, lat.Seconds(), strings.Join(served, "+"))
			if err != nil {
				fmt.Fprintf(out, "ERROR: %v\n\n", err)
				continue
			}
			fmt.Fprintf(out, "```\n%s\n```\n\n", strings.TrimSpace(text))
		}
	}
	fmt.Fprintf(out, "\nTotal cost $%.4f\n", total)
	return 0
}

type aiEvalCase struct {
	route ai.Route
	name  string
	call  func(ctx context.Context, a *ai.Analyzer) (string, ai.TokenUsage, error)
}

// aiEvalCases builds n inputs for each of the three routes from the demo account's shapes.
func aiEvalCases(n int) ([]aiEvalCase, error) {
	var out []aiEvalCase
	drv := evalDrives()
	for i := 0; i < n && i < len(drv); i++ {
		d := drv[i]
		in := recap.Inputs{Drive: d.d, Samples: d.s, UseFahrenheit: true, BaselineMiPerKWh: 2.3, BaselineDays: 30, Weather: d.w}
		out = append(out, aiEvalCase{ai.RouteRecap, d.name, func(ctx context.Context, a *ai.Analyzer) (string, ai.TokenUsage, error) {
			r, err := recap.Generate(ctx, a, in)
			return r.Recap, ai.TokenUsage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens}, err
		}})
	}
	for i := 0; i < n && i < len(drv); i++ {
		d := drv[i]
		in := recap.EfficiencyInputs{Drive: d.d, Samples: d.s, UseFahrenheit: true, BaselineMiPerKWh: 2.3, BaselineDays: 30, Weather: d.w,
			VehicleProfile: d.profile, ExtraLoadLb: d.loadLb, Towing: d.towing}
		out = append(out, aiEvalCase{ai.RouteEfficiency, d.name, func(ctx context.Context, a *ai.Analyzer) (string, ai.TokenUsage, error) {
			r, err := recap.GenerateEfficiency(ctx, a, in)
			return r.Analysis, ai.TokenUsage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens}, err
		}})
	}
	fx, err := demo.PlanFixtures()
	if err != nil {
		return nil, err
	}
	if len(fx) == 0 {
		return nil, fmt.Errorf("no demo plan fixtures")
	}
	for i := 0; i < n; i++ {
		f := fx[i%len(fx)]
		plan := f.Plan
		tc := tripadvice.Context{StartingSoC: f.StartSoC, HasAdapter: i%2 == 0, PackKWh: 131, HomePricePerKWh: 0.16, HomeCurrency: "USD",
			GasPricePerGallon: 3.6, ComparisonMPG: 26, TirePlacardPSI: 48, Now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
		if i >= 2 {
			tc.Weather = &weather.Snapshot{TempC: []float64{-6, 33, 2}[i-2], HasTemp: true, WindKPH: 30, HasWind: true, HeadwindKPH: 20, Conditions: "Overcast", HasConditions: true}
		}
		if i == 1 {
			tc.DriveMode = "conserve"
		}
		name := fmt.Sprintf("%s (start %.0f%%)", f.Name, f.StartSoC)
		out = append(out, aiEvalCase{ai.RouteAdvice, name, func(ctx context.Context, a *ai.Analyzer) (string, ai.TokenUsage, error) {
			r, err := tripadvice.Generate(ctx, a, &plan, tc)
			return r.Raw, ai.TokenUsage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens}, err
		}})
	}
	return out, nil
}

type evalDrive struct {
	name    string
	d       drives.Drive
	s       []samples.Sample
	w       *recap.Weather
	profile *recap.VehicleProfile
	loadLb  float64
	towing  bool
}

// evalDrives are five made-up drives that differ in what a recap or an efficiency read should notice.
func evalDrives() []evalDrive {
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	mk := func(name string, miles, kwh, mins, climbM, tempC, startSoC float64) evalDrive {
		start := base.Add(time.Duration(len(name)) * time.Hour)
		drop := kwh / 131 * 100
		d := drives.Drive{
			ID: "eval_" + strings.ReplaceAll(name, " ", "_"), VehicleID: "demo", StartedAt: start, EndedAt: start.Add(time.Duration(mins) * time.Minute),
			StartSoCPct: startSoC, EndSoCPct: startSoC - drop, DistanceMi: miles, EnergyUsedKWh: kwh,
			MaxSpeedMph: 74, AvgSpeedMph: miles / (mins / 60), StartLat: demo.HomeLat, StartLon: demo.HomeLon, EndLat: demo.HomeLat + 0.2, EndLon: demo.HomeLon + 0.3,
		}
		var ss []samples.Sample
		const n = 30
		for i := 0; i <= n; i++ {
			f := float64(i) / n
			alt := 300 + climbM*math.Sin(f*math.Pi/2)
			tp := 2.6 // bar
			ss = append(ss, samples.Sample{
				VehicleID: "demo", At: start.Add(time.Duration(f*mins) * time.Minute), BatteryLevelPct: startSoC - drop*f, OdometerMi: 10000 + miles*f,
				Lat: demo.HomeLat + 0.2*f, Lon: demo.HomeLon + 0.3*f, SpeedMph: d.AvgSpeedMph * (0.7 + 0.6*math.Abs(math.Sin(f*9))), ShiftState: "D",
				DriveMode: "everyday", OutsideTempC: tempC, InsideTempC: 21, AltitudeM: &alt, TirePressureMinBar: &tp,
			})
		}
		return evalDrive{name: name, d: d, s: ss}
	}
	commute := mk("short city commute", 11, 4.6, 24, 20, 17, 78)
	highway := mk("long cold highway run", 142, 74, 128, 80, -4, 96)
	highway.w = &recap.Weather{TempC: -4, HasTemp: true, WindKPH: 38, HasWind: true, HeadwindKPH: 28, HasHeadwind: true, Conditions: "Snow", HasConditions: true}
	mountain := mk("mountain climb", 58, 31, 71, 1100, 6, 88)
	tow := mk("towing a trailer", 64, 52, 80, 120, 24, 90)
	tow.towing, tow.loadLb = true, 1800
	tow.profile = &recap.VehicleProfile{TireType: "all-terrain", WheelInches: 20, Accessories: []string{"roof rack"}, FrequentlyTows: true, TirePlacardPSI: 48}
	easy := mk("efficient summer errand", 23, 7.4, 41, 10, 27, 71)
	easy.w = &recap.Weather{TempC: 27, HasTemp: true, WindKPH: 8, HasWind: true, Conditions: "Clear", HasConditions: true}
	return []evalDrive{commute, highway, mountain, tow, easy}
}

// servedCost prices a call by the model the responses report, not the one requested, so a silent alias change cannot hide
// in the total. With several requests in one call it takes the dearest; never below the requested model.
func servedCost(requested string, served []string, usage ai.TokenUsage) float64 {
	// The requested model is the floor: an unknown served id prices on the fallback rate and must not come out below it.
	cost := ai.ComputeCost("anthropic", requested, usage.InputTokens, usage.OutputTokens)
	for _, m := range served {
		cost = max(cost, ai.ComputeCost("anthropic", m, usage.InputTokens, usage.OutputTokens))
	}
	return cost
}
