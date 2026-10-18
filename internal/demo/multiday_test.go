package demo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

func TestSampleMultidayTrip(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cfg, nets := settings.ChargingConfig{HomePricePerKWh: HomeRate, HomeCurrency: "USD"}, settings.DefaultChargingNetworks()
	p, err := SampleMultidayPlan(now, time.Time{}, cfg, nets)
	if err != nil || p == nil {
		t.Fatalf("plan %v err %v", p, err)
	}
	if p.ID != SampleMultidayTripID() || p.Name != MultidayTripName {
		t.Errorf("id/name %s %q", p.ID, p.Name)
	}
	var saved struct {
		Kind string `json:"kind"`
		Days []struct {
			Index     int             `json:"index"`
			Plan      json.RawMessage `json:"plan"`
			DepartAt  string          `json:"depart_at"`
			ArriveAt  string          `json:"arrive_at"`
			Start     struct{ Latitude, Longitude float64 }
			End       struct{ Latitude, Longitude float64 }
			Costs     []json.RawMessage `json:"costs"`
			Overnight *struct {
				Name             string  `json:"name"`
				ParkedHours      float64 `json:"parked_hours"`
				L2KW             float64 `json:"l2_kw"`
				AddedKWh         float64 `json:"added_kwh"`
				PostChargeSoCPct float64 `json:"post_charge_soc_pct"`
				Capped           bool    `json:"capped"`
			} `json:"overnight"`
			DepartureSoC float64 `json:"departure_soc"`
			ArrivalSoC   float64 `json:"arrival_soc"`
		} `json:"days"`
		Total struct {
			DistanceMeters float64 `json:"distance_meters"`
		} `json:"total"`
	}
	if err := json.Unmarshal(p.Plan, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Kind != "multiday" || len(saved.Days) != 2 || saved.Total.DistanceMeters <= 0 {
		t.Fatalf("saved plan: %+v", saved)
	}
	d1, d2 := saved.Days[0], saved.Days[1]
	if d1.Index != 1 || d2.Index != 2 || len(d1.Costs) != 1 || len(d2.Costs) != 1 {
		t.Errorf("days: %+v", saved.Days)
	}
	dep1, _ := time.Parse(time.RFC3339, d1.DepartAt)
	arr1, _ := time.Parse(time.RFC3339, d1.ArriveAt)
	dep2, _ := time.Parse(time.RFC3339, d2.DepartAt)
	if !(dep1.Before(arr1) && arr1.Before(dep2)) {
		t.Errorf("times not in order: %s %s %s", d1.DepartAt, d1.ArriveAt, d2.DepartAt)
	}
	ov := d1.Overnight
	if ov == nil || ov.Name == "" || ov.L2KW != 7 || ov.ParkedHours <= 0 || ov.AddedKWh <= 0 || ov.PostChargeSoCPct != d2.DepartureSoC || ov.PostChargeSoCPct <= d1.ArrivalSoC {
		t.Errorf("overnight %+v (day 1 arrives %v, day 2 leaves %v)", ov, d1.ArrivalSoC, d2.DepartureSoC)
	}
	if d2.Overnight != nil {
		t.Error("the last day has an overnight")
	}
	// Day 1 ends where day 2 starts: Las Vegas.
	if d1.End.Latitude != d2.Start.Latitude || d1.End.Longitude != d2.Start.Longitude {
		t.Errorf("day 1 ends %+v, day 2 starts %+v", d1.End, d2.Start)
	}
	// The inputs are a multi-day planner form with one overnight.
	var in struct {
		ExtraStops []json.RawMessage `json:"extra_stops"`
		Flags      []bool            `json:"overnight_flags"`
		Settings   []struct {
			Until string  `json:"until"`
			L2KW  float64 `json:"l2_kw"`
		} `json:"overnight_settings"`
	}
	if err := json.Unmarshal(p.Inputs, &in); err != nil || len(in.ExtraStops) != 1 || len(in.Flags) != 1 || !in.Flags[0] || len(in.Settings) != 1 || in.Settings[0].L2KW != 7 || in.Settings[0].Until == "" {
		t.Errorf("inputs %s (%v)", p.Inputs, err)
	}
	// The first day departs where the replay drove it (no time shift for the saved trip).
	out := rivian.SampleTripPlans(now, time.Time{})
	if len(out) < 2 || !p.DepartAt.Equal(out[0].DepartAt.UTC()) {
		t.Errorf("saved depart %v, leg depart %v", p.DepartAt, out[0].DepartAt)
	}
}

func TestMultidayPresetMatchesTheSavedTrip(t *testing.T) {
	f, err := vegasMultidayPreset()
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != SampleMultidayTripID().String() || f.Name != MultidayTripName {
		t.Errorf("preset %s %q", f.ID, f.Name)
	}
	var resp struct {
		Days []struct {
			Index     int `json:"index"`
			Overnight any `json:"overnight"`
		} `json:"days"`
		TZ string `json:"tz"`
	}
	if err := json.Unmarshal(f.Plan, &resp); err != nil || len(resp.Days) != 2 || resp.TZ != planLocation || resp.Days[0].Overnight == nil {
		t.Errorf("preset plan: %+v (%v)", resp, err)
	}
	if f.Summary.DistanceMi < 400 || f.Summary.ChargeStops < 1 {
		t.Errorf("summary %+v", f.Summary)
	}
}

// The checked-in two-day preset is what the corpus builds now: a corpus change
// must regenerate it (go test -tags demotripsgen, see presets_gen_test.go).
func TestMultidayPresetIsCurrent(t *testing.T) {
	f, err := vegasMultidayPreset()
	if err != nil {
		t.Fatal(err)
	}
	var p Preset
	for _, q := range readPresetIndex(t).Trips {
		if q.ID == f.ID {
			p = q
		}
	}
	if p.ID == "" {
		t.Fatal("the two-day trip is not a preset")
	}
	b, err := os.ReadFile(filepath.Join(PresetsDir, f.ID+".plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if compact(b) != compact(f.Plan) || p.Name != f.Name || compact(p.Inputs) != compact(f.Inputs) || p.Summary != f.Summary {
		t.Error("the two-day preset differs from what the corpus builds; regenerate")
	}
}
