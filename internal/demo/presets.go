package demo

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// The demo planner's preset trips (demo parity): stored planner
// responses the anonymous demo serves instead of calling Rivian's
// planner (GET /api/demo/trips, /api/demo/trips/{id}/plan). The data
// lives in internal/demo/trips: index.json lists the presets, and
// <id>.plan.json holds each one's response. The Las Vegas presets (each
// leg, and both as one two-day trip whose plan file is the saved
// multi-day snapshot) are the sample owner's saved plans (the same ids, SampleTripID, so the
// demo can open them with Plan vs Actual) and are built from the corpus here; the others
// are generated from Valhalla routes, the chargers archive and the
// sample truck's efficiency by presets_gen_test.go (run by hand).

// PresetsDir is where the preset files live, relative to this package.
const PresetsDir = "trips"

// presetDepart is when every preset leaves: a Saturday, 08:00 in the
// sample truck's zone. The planner shows the plan's own times unless a
// visitor picks a departure.
func presetDepart() (time.Time, error) {
	loc, err := time.LoadLocation(planLocation)
	if err != nil {
		return time.Time{}, fmt.Errorf("demo: plan location: %w", err)
	}
	return time.Date(2026, 11, 14, 8, 0, 0, 0, loc), nil
}

// presetPlansAt is a time when the sample's Las Vegas plans exist; the
// Las Vegas presets' content doesn't depend on which loop it picks.
var presetPlansAt = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// PresetIndex is index.json.
type PresetIndex struct {
	Trips []Preset `json:"trips"`
}

// Preset is one preset trip in index.json.
type Preset struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Inputs  json.RawMessage `json:"inputs"`
	Summary PresetSummary   `json:"summary"`
	// RoutePolyline is the route's road shape (Valhalla's, simplified)
	// through every waypoint, as a Google encoded polyline of precision
	// 6, for the map: the demo can't ask Valhalla for it.
	RoutePolyline string `json:"route_polyline"`
}

// PresetSummary is what the preset list shows.
type PresetSummary struct {
	DistanceMi    float64 `json:"distance_mi"`
	DurationMin   float64 `json:"duration_min"`
	ChargeStops   int     `json:"charge_stops"`
	ArrivalSoCPct float64 `json:"arrival_soc_pct"`
}

// presetFile is a preset with its plan response.
type presetFile struct {
	Preset
	Plan json.RawMessage
}

// PresetCostsAt is the date the preset plans' costs were priced: the observed
// third-party prices in them (dcfcrates, hand-copied from DCFC Tracker) were checked
// on this day, and the presets are static files, so they are built "as of" it and
// the test that compares them with the corpus stays deterministic.
//
// REFRESH BY OCTOBER 2027: an observed price is hidden from live plans 12 months
// after its month, but a preset file keeps showing the figure it was built with (with
// its month in the label). When the figures in dcfcrates/observed.go are re-checked,
// move this date, regenerate the preset plans (see presets_gen_test.go) and the
// sample-trip pages built from them; if they are not re-checked, regenerate with no
// observed prices, so the demo stops showing a figure the live planner would hide.
var PresetCostsAt = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

// newPreset builds a preset from a plan.
func newPreset(id, name string, from, to rivian.SamplePlace, startSoC, targetSoC, packKWh float64, plan rivian.TripPlan) (presetFile, error) {
	if len(plan.Routes) == 0 {
		return presetFile{}, fmt.Errorf("demo: preset %s has no route", id)
	}
	in, err := plannerInputs(from, to, startSoC, targetSoC, time.Time{})
	if err != nil {
		return presetFile{}, err
	}
	pb, err := plannerResponse(plan, packKWh, presetConfig(), settings.DefaultChargingNetworks(), PresetCostsAt)
	if err != nil {
		return presetFile{}, err
	}
	r := plan.Routes[0]
	stops := 0
	for _, w := range r.Waypoints {
		if w.WaypointType == "DC_CHARGE_STATION" {
			stops++
		}
	}
	return presetFile{Preset: Preset{ID: id, Name: name, Inputs: in, Summary: PresetSummary{
		DistanceMi:    math.Round(r.TotalDriveDistanceMeters/1609.344*10) / 10,
		DurationMin:   math.Round(float64(r.TotalTripDurationSec) / 60),
		ChargeStops:   stops,
		ArrivalSoCPct: r.ArrivalSoC,
	}}, Plan: pb}, nil
}

// presetConfig is the demo user's charging settings (Seed).
func presetConfig() settings.ChargingConfig {
	return settings.ChargingConfig{HomePricePerKWh: HomeRate, HomeCurrency: "USD"}
}

// vegasPresets are the sample owner's Las Vegas plans as presets: the
// saved trips' ids and plans, with the times moved to presetDepart.
func vegasPresets() ([]presetFile, error) {
	depart, err := presetDepart()
	if err != nil {
		return nil, err
	}
	var out []presetFile
	for _, p := range rivian.SampleTripPlans(presetPlansAt, time.Time{}) {
		plan := shiftPlan(p.Plan, depart.Sub(p.DepartAt))
		f, err := newPreset(SampleTripID(p.Name).String(), p.Name, p.Origin, p.Destination, p.StartSoC, p.TargetSoC, p.PackKWh, plan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// twoDayPreset is the sample owner's two-day Las Vegas trip as a preset:
// the saved trip's id and multi-day snapshot, with the times moved to
// presetDepart. shapes are each day's road shape (the Las Vegas presets'
// own, as both days drive those routes), carried on the day's route; the
// preset itself has none, as there is no single route to draw.
func twoDayPreset(shapes []string) (presetFile, error) {
	depart, err := presetDepart()
	if err != nil {
		return presetFile{}, err
	}
	days := rivian.SampleTwoDayPlan(presetPlansAt, time.Time{})
	if len(days) != 2 {
		return presetFile{}, fmt.Errorf("demo: no two-day Las Vegas plan")
	}
	d := depart.Sub(days[0].DepartAt)
	for i := range days {
		days[i].Plan = shiftPlan(days[i].Plan, d)
		days[i].DepartAt, days[i].SavedAt = days[i].DepartAt.Add(d), days[i].SavedAt.Add(d)
	}
	in, err := twoDayInputs(days)
	if err != nil {
		return presetFile{}, err
	}
	pb, err := twoDaySnapshot(days, presetConfig(), settings.DefaultChargingNetworks(), shapes, PresetCostsAt)
	if err != nil {
		return presetFile{}, err
	}
	var sum PresetSummary
	var meters float64
	var secs int
	for _, day := range days {
		r := day.Plan.Routes[0]
		meters += r.TotalDriveDistanceMeters
		secs += r.TotalTripDurationSec
		for _, w := range r.Waypoints {
			if w.WaypointType == "DC_CHARGE_STATION" {
				sum.ChargeStops++
			}
		}
		sum.ArrivalSoCPct = r.ArrivalSoC
	}
	sum.DistanceMi = math.Round(meters/1609.344*10) / 10
	sum.DurationMin = math.Round(float64(secs) / 60)
	return presetFile{Preset: Preset{ID: SampleTripID(rivian.SamplePlanVegasTwoDay).String(), Name: rivian.SamplePlanVegasTwoDay,
		Inputs: in, Summary: sum}, Plan: pb}, nil
}

// shiftPlan moves every waypoint time in plan by d.
func shiftPlan(plan rivian.TripPlan, d time.Duration) rivian.TripPlan {
	shift := func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
		return t.Add(d).UTC().Format(time.RFC3339)
	}
	routes := make([]rivian.TripRoute, len(plan.Routes))
	for i, r := range plan.Routes {
		wps := make([]rivian.PlannedWaypoint, len(r.Waypoints))
		for j, w := range r.Waypoints {
			w.ArrivalTimeUTC, w.DepartureTimeUTC = shift(w.ArrivalTimeUTC), shift(w.DepartureTimeUTC)
			wps[j] = w
		}
		r.Waypoints = wps
		routes[i] = r
	}
	plan.Routes = routes
	return plan
}
