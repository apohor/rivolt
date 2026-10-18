package demo

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/apohor/rivolt/internal/rivian"
)

// PlanFixtures turns the preset trips into stored plans for the fixture
// planner (rivian.FixturePlanner): each preset's origin, destination and
// starting SoC with its plan. Presets with intermediate stops are left out,
// as the fixture planner matches two-point trips only.
func PlanFixtures() ([]rivian.PlanFixture, error) {
	if err := PresetsErr(); err != nil {
		return nil, err
	}
	var idx struct {
		Trips []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Inputs struct {
				Origin      struct{ Lat, Lon float64 } `json:"origin"`
				Destination struct{ Lat, Lon float64 } `json:"destination"`
				ExtraStops  []json.RawMessage          `json:"extra_stops"`
				StartingSoC string                     `json:"starting_soc"`
			} `json:"inputs"`
		} `json:"trips"`
	}
	if err := json.Unmarshal(PresetsIndex().Body, &idx); err != nil {
		return nil, fmt.Errorf("demo: preset index: %w", err)
	}
	var out []rivian.PlanFixture
	for _, t := range idx.Trips {
		if len(t.Inputs.ExtraStops) > 0 {
			continue
		}
		soc, err := strconv.ParseFloat(t.Inputs.StartingSoC, 64)
		if err != nil {
			return nil, fmt.Errorf("demo: preset %s: starting_soc: %w", t.ID, err)
		}
		blob, ok := PresetPlan(t.ID)
		if !ok {
			return nil, fmt.Errorf("demo: preset %s has no plan", t.ID)
		}
		var plan rivian.TripPlan
		if err := json.Unmarshal(blob.Body, &plan); err != nil {
			return nil, fmt.Errorf("demo: preset %s plan: %w", t.ID, err)
		}
		out = append(out, rivian.PlanFixture{
			Name:        t.Name,
			Origin:      [2]float64{t.Inputs.Origin.Lat, t.Inputs.Origin.Lon},
			Destination: [2]float64{t.Inputs.Destination.Lat, t.Inputs.Destination.Lon},
			StartSoC:    soc,
			Plan:        plan,
		})
	}
	return out, nil
}
