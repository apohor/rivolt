package demo

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestPlanFixturesServeThePresets(t *testing.T) {
	fx, err := PlanFixtures()
	if err != nil {
		t.Fatal(err)
	}
	// Every preset but the two-day trip, which has an overnight stop.
	if len(fx) != len(PresetIDs())-1 || len(fx) < 4 {
		t.Fatalf("%d fixtures for %d presets", len(fx), len(PresetIDs()))
	}
	p := rivian.NewFixturePlanner(fx, func() time.Time { return time.Date(2026, 12, 5, 8, 0, 0, 0, time.UTC) })
	for _, f := range fx {
		in := rivian.PlanTripInput{StartingSoC: f.StartSoC, Waypoints: []rivian.PlanTripWaypoint{
			{Latitude: f.Origin[0], Longitude: f.Origin[1], WaypointType: "origin"},
			{Latitude: f.Destination[0], Longitude: f.Destination[1], WaypointType: "destination"},
		}}
		got, err := p.PlanTrip(context.Background(), in)
		if err != nil {
			t.Errorf("%s: %v", f.Name, err)
			continue
		}
		if !got.Simulated || len(got.Routes) == 0 || len(got.Routes[0].Waypoints) < 2 {
			t.Errorf("%s: plan = %+v", f.Name, got)
			continue
		}
		w := got.Routes[0].Waypoints
		if w[0].DepartureTimeUTC != "2026-12-05T08:00:00Z" {
			t.Errorf("%s: departs %s, want the pinned clock", f.Name, w[0].DepartureTimeUTC)
		}
		// The fixture's own durations survive: origin departure to destination arrival.
		o, _ := time.Parse(time.RFC3339, w[0].DepartureTimeUTC)
		d, _ := time.Parse(time.RFC3339, w[len(w)-1].ArrivalTimeUTC)
		sf := f.Plan.Routes[0].Waypoints
		so, _ := time.Parse(time.RFC3339, sf[0].DepartureTimeUTC)
		sd, _ := time.Parse(time.RFC3339, sf[len(sf)-1].ArrivalTimeUTC)
		if d.Sub(o) != sd.Sub(so) {
			t.Errorf("%s: trip time %v, stored %v", f.Name, d.Sub(o), sd.Sub(so))
		}
	}
}
