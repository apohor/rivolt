package rivian

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func fixtureForTest() PlanFixture {
	return PlanFixture{
		Name:        "A to B",
		Origin:      [2]float64{34.1613, -118.1676},
		Destination: [2]float64{36.1699, -115.1398},
		StartSoC:    90,
		Plan: TripPlan{Status: "OK", ChargeStationsAvailable: true, Routes: []TripRoute{{
			DestinationReached: true, ArrivalSoC: 30, ArrivalReachableMeters: 90_000,
			Waypoints: []PlannedWaypoint{
				{WaypointType: "ORIGIN", DepartureSoC: 90, DepartureReachableMeters: 270_000, DepartureTimeUTC: "2026-11-14T16:00:00Z"},
				{WaypointType: "DC_CHARGE_STATION", ArrivalSoC: 20, ArrivalReachableMeters: 60_000, DepartureSoC: 80, DepartureReachableMeters: 240_000,
					ArrivalTimeUTC: "2026-11-14T18:30:00Z", DepartureTimeUTC: "2026-11-14T19:00:00Z", ChargeDurationSec: 1800},
				{WaypointType: "DESTINATION", ArrivalSoC: 30, ArrivalReachableMeters: 90_000, ArrivalTimeUTC: "2026-11-14T20:00:00Z"},
			},
		}}},
	}
}

func inputFor(f PlanFixture, soc float64) PlanTripInput {
	return PlanTripInput{StartingSoC: soc, Waypoints: []PlanTripWaypoint{
		{Latitude: f.Origin[0], Longitude: f.Origin[1], WaypointType: "origin"},
		{Latitude: f.Destination[0], Longitude: f.Destination[1], WaypointType: "destination"},
	}}
}

func TestFixturePlannerMatchesAndShifts(t *testing.T) {
	f := fixtureForTest()
	now := time.Date(2026, 12, 1, 9, 30, 45, 0, time.UTC)
	p := NewFixturePlanner([]PlanFixture{f}, func() time.Time { return now })

	// Same trip, same SoC, no departure given: leaves at Now (to the minute);
	// every time moves by the same amount, durations stay the fixture's.
	got, err := p.PlanTrip(context.Background(), inputFor(f, 90))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Simulated {
		t.Error("a fixture plan must be marked simulated")
	}
	w := got.Routes[0].Waypoints
	if w[0].DepartureTimeUTC != "2026-12-01T09:30:00Z" || w[1].ArrivalTimeUTC != "2026-12-01T12:00:00Z" ||
		w[1].DepartureTimeUTC != "2026-12-01T12:30:00Z" || w[2].ArrivalTimeUTC != "2026-12-01T13:30:00Z" {
		t.Errorf("times = %s %s %s %s", w[0].DepartureTimeUTC, w[1].ArrivalTimeUTC, w[1].DepartureTimeUTC, w[2].ArrivalTimeUTC)
	}
	if w[1].ChargeDurationSec != 1800 {
		t.Error("durations must stay the fixture's")
	}

	// An explicit departure wins over Now.
	in := inputFor(f, 90)
	in.DepartureTime = time.Date(2026, 11, 20, 14, 15, 0, 0, time.FixedZone("x", -5*3600))
	got, _ = p.PlanTrip(context.Background(), in)
	if d := got.Routes[0].Waypoints[0].DepartureTimeUTC; d != "2026-11-20T19:15:00Z" {
		t.Errorf("explicit departure = %s, want 2026-11-20T19:15:00Z", d)
	}

	// Determinism, and the stored plan is never touched.
	again, _ := p.PlanTrip(context.Background(), in)
	if !reflect.DeepEqual(got, again) {
		t.Error("same input must give the same plan")
	}
	if f.Plan.Routes[0].Waypoints[0].DepartureTimeUTC != "2026-11-14T16:00:00Z" || f.Plan.Simulated {
		t.Error("the fixture was mutated")
	}
}

func TestFixturePlannerShiftsSoCWithTheRequestedStart(t *testing.T) {
	f := fixtureForTest()
	p := NewFixturePlanner([]PlanFixture{f}, func() time.Time { return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) })
	got, err := p.PlanTrip(context.Background(), inputFor(f, 86)) // 4 points lower
	if err != nil {
		t.Fatal(err)
	}
	r := got.Routes[0]
	if r.Waypoints[0].DepartureSoC != 86 || r.Waypoints[1].ArrivalSoC != 16 || r.Waypoints[1].DepartureSoC != 76 || r.ArrivalSoC != 26 {
		t.Errorf("SoC = %v %v %v %v, want 86 16 76 26", r.Waypoints[0].DepartureSoC, r.Waypoints[1].ArrivalSoC, r.Waypoints[1].DepartureSoC, r.ArrivalSoC)
	}
	// Reach follows SoC (linear): 270000/90 = 3000 m per point.
	if r.Waypoints[0].DepartureReachableMeters != 258_000 || r.ArrivalReachableMeters != 78_000 {
		t.Errorf("reach = %v %v, want 258000 78000", r.Waypoints[0].DepartureReachableMeters, r.ArrivalReachableMeters)
	}
	// Clamped at 0 and 100.
	got, _ = p.PlanTrip(context.Background(), inputFor(f, 94))
	if got.Routes[0].Waypoints[1].DepartureSoC != 84 {
		t.Error("a +4 shift")
	}
	f2 := fixtureForTest()
	f2.StartSoC = 98
	f2.Plan.Routes[0].Waypoints[0].DepartureSoC = 98
	p2 := NewFixturePlanner([]PlanFixture{f2}, nil)
	got, _ = p2.PlanTrip(context.Background(), inputFor(f2, 100))
	if got.Routes[0].Waypoints[1].DepartureSoC != 82 { // 80 + 2
		t.Errorf("shift near the top = %v", got.Routes[0].Waypoints[1].DepartureSoC)
	}
}

func TestFixturePlannerNoMatch(t *testing.T) {
	f := fixtureForTest()
	p := NewFixturePlanner([]PlanFixture{f}, nil)
	far := inputFor(f, 90)
	far.Waypoints[0].Latitude += 0.02 // ~2.2 km
	nearEnough := inputFor(f, 90)
	nearEnough.Waypoints[0].Latitude += 0.005 // ~0.55 km
	withVia := inputFor(f, 90)
	withVia.Waypoints = []PlanTripWaypoint{withVia.Waypoints[0], {Latitude: 35, Longitude: -117, WaypointType: "waypoint"}, withVia.Waypoints[1]}
	for name, in := range map[string]PlanTripInput{
		"origin too far":        far,
		"soc just outside":      inputFor(f, 84.9),
		"soc far outside":       inputFor(f, 50),
		"via":                   withVia,
		"one waypoint":          {StartingSoC: 90, Waypoints: inputFor(f, 90).Waypoints[:1]},
		"no waypoints":          {StartingSoC: 90},
		"destination elsewhere": func() PlanTripInput { i := inputFor(f, 90); i.Waypoints[1].Latitude = 40; return i }(),
	} {
		if _, err := p.PlanTrip(context.Background(), in); !errors.Is(err, ErrNoFixture) {
			t.Errorf("%s: err = %v, want ErrNoFixture", name, err)
		}
	}
	for name, in := range map[string]PlanTripInput{"within 1 km": nearEnough, "soc at the edge": inputFor(f, 85)} {
		if _, err := p.PlanTrip(context.Background(), in); err != nil {
			t.Errorf("%s should match: %v", name, err)
		}
	}
}

func TestFixturePlannerPicksTheNearest(t *testing.T) {
	a, b := fixtureForTest(), fixtureForTest()
	a.Name, b.Name = "a", "b"
	b.Origin[0] += 0.004 // ~0.45 km from a
	b.Plan.Routes[0].TotalDriveDurationSec = 777
	p := NewFixturePlanner([]PlanFixture{a, b}, nil)
	in := inputFor(b, 90) // exactly b's origin
	got, _ := p.PlanTrip(context.Background(), in)
	if got.Routes[0].TotalDriveDurationSec != 777 {
		t.Error("should pick the fixture closest to the request")
	}
}
