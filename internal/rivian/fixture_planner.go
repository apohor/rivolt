package rivian

import (
	"context"
	"errors"
	"math"
	"time"
)

// ErrNoFixture means no stored plan matches the request.
var ErrNoFixture = errors.New("rivian: no stored plan matches this trip")

// Match tolerances: each end of the trip within fixtureMatchKm of the
// fixture's, and the starting SoC within fixtureSoCWindow points.
const (
	fixtureMatchKm   = 1.0
	fixtureSoCWindow = 5.0
)

// PlanFixture is one stored plan and the trip it answers.
type PlanFixture struct {
	Name        string
	Origin      [2]float64 // lat, lon
	Destination [2]float64
	StartSoC    float64
	Plan        TripPlan
}

// FixturePlanner answers a trip with a stored plan when one matches: origin
// and destination each within 1 km of a fixture's, no intermediate stops, and
// a starting SoC within 5 points of the fixture's. The stored plan is moved
// to the requested departure, and its SoC values to the requested start, and
// it is marked Simulated. No match is ErrNoFixture. Deterministic: the only
// clock is Now, which tests pin.
type FixturePlanner struct {
	fixtures []PlanFixture
	// Now stands in for "now" when the input has no departure time.
	Now func() time.Time
}

var _ TripPlanner = (*FixturePlanner)(nil)

// NewFixturePlanner wraps the fixtures. now may be nil (time.Now).
func NewFixturePlanner(fx []PlanFixture, now func() time.Time) *FixturePlanner {
	if now == nil {
		now = time.Now
	}
	return &FixturePlanner{fixtures: fx, Now: now}
}

// PlanTrip implements TripPlanner.
func (p *FixturePlanner) PlanTrip(_ context.Context, in PlanTripInput) (*TripPlan, error) {
	o, d, ok := endpoints(in)
	if !ok {
		return nil, ErrNoFixture
	}
	// A stored plan was made for the default route; one that avoids something
	// is a different route, so it is routed (the next planner), not replayed.
	if in.RoutePreferences.Any() {
		return nil, ErrNoFixture
	}
	best, bestDist := -1, math.MaxFloat64
	for i, f := range p.fixtures {
		do := haversineM(o[0], o[1], f.Origin[0], f.Origin[1]) / 1000
		dd := haversineM(d[0], d[1], f.Destination[0], f.Destination[1]) / 1000
		if do > fixtureMatchKm || dd > fixtureMatchKm || math.Abs(in.StartingSoC-f.StartSoC) > fixtureSoCWindow {
			continue
		}
		if do+dd < bestDist {
			best, bestDist = i, do+dd
		}
	}
	if best < 0 {
		return nil, ErrNoFixture
	}
	f := p.fixtures[best]
	depart := in.DepartureTime
	if depart.IsZero() {
		depart = p.Now().Truncate(time.Minute)
	}
	out := shiftFixture(f, depart.UTC(), in.StartingSoC)
	out.Simulated = true
	return &out, nil
}

// endpoints returns the origin and destination of a trip with no
// intermediate stops.
func endpoints(in PlanTripInput) (o, d [2]float64, ok bool) {
	if len(in.Waypoints) != 2 {
		return o, d, false
	}
	a, b := in.Waypoints[0], in.Waypoints[1]
	return [2]float64{a.Latitude, a.Longitude}, [2]float64{b.Latitude, b.Longitude}, true
}

// shiftFixture returns a copy of the fixture's plan departing at depart and
// starting at startSoC: waypoint times move by the same amount (durations are
// the fixture's), every SoC moves by the difference between the requested and
// the fixture's start (clamped to 0-100), and the reach figures follow their
// SoC.
func shiftFixture(f PlanFixture, depart time.Time, startSoC float64) TripPlan {
	plan := f.Plan
	delta := startSoC - f.StartSoC
	var dt time.Duration
	var haveDt bool
	routes := make([]TripRoute, len(plan.Routes))
	for i, r := range plan.Routes {
		wps := make([]PlannedWaypoint, len(r.Waypoints))
		copy(wps, r.Waypoints)
		if !haveDt && len(wps) > 0 {
			if t0, err := time.Parse(time.RFC3339, wps[0].DepartureTimeUTC); err == nil {
				dt, haveDt = depart.Sub(t0), true
			}
		}
		for j := range wps {
			w := &wps[j]
			w.ArrivalTimeUTC = shiftStamp(w.ArrivalTimeUTC, dt)
			w.DepartureTimeUTC = shiftStamp(w.DepartureTimeUTC, dt)
			w.ArrivalReachableMeters = scaleReach(w.ArrivalReachableMeters, w.ArrivalSoC, delta)
			w.DepartureReachableMeters = scaleReach(w.DepartureReachableMeters, w.DepartureSoC, delta)
			w.ArrivalSoC = clampSoC(w.ArrivalSoC + delta)
			w.DepartureSoC = clampSoC(w.DepartureSoC + delta)
		}
		r.Waypoints = wps
		r.ArrivalReachableMeters = scaleReach(r.ArrivalReachableMeters, r.ArrivalSoC, delta)
		r.ArrivalSoC = clampSoC(r.ArrivalSoC + delta)
		routes[i] = r
	}
	plan.Routes = routes
	return plan
}

func shiftStamp(s string, d time.Duration) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Add(d).UTC().Format(time.RFC3339)
}

func clampSoC(v float64) float64 { return math.Round(math.Min(100, math.Max(0, v))*10) / 10 }

// scaleReach moves a reach figure with its SoC: reach is linear in SoC (the
// same relation sample_plan.go uses), so the per-point ratio is kept.
func scaleReach(reach, soc, delta float64) float64 {
	if soc <= 0 || reach <= 0 {
		return reach
	}
	return math.Round(reach/soc*clampSoC(soc+delta)*10) / 10
}
