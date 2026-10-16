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
// <id>.plan.json holds each one's response. The Las Vegas presets are
// the sample owner's saved plans (the same ids, SampleTripID, so the
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
	// Shape is the route's road shape through every waypoint, as an
	// encoded polyline (precision 6), for the map: the demo can't ask
	// Valhalla for it.
	Shape string `json:"shape"`
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

// newPreset builds a preset from a plan.
func newPreset(id, name string, from, to rivian.SamplePlace, startSoC, targetSoC, packKWh float64, plan rivian.TripPlan) (presetFile, error) {
	if len(plan.Routes) == 0 {
		return presetFile{}, fmt.Errorf("demo: preset %s has no route", id)
	}
	in, err := plannerInputs(from, to, startSoC, targetSoC, time.Time{})
	if err != nil {
		return presetFile{}, err
	}
	pb, err := plannerResponse(plan, packKWh, presetConfig(), settings.DefaultChargingNetworks())
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
