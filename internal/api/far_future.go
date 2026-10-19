package api

import (
	"context"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// A trip can be planned for a departure months ahead (up to maxFutureDeparture),
// but Rivian's gateway is only ever asked about departures a person would
// plausibly pick: now through a few weeks out. For a request whose start is
// more than farFutureAfter away, the planner is wrapped so that every departure
// it passes down is moved back by one shift S = start - now (day 1 then departs
// "now", which the gateway query leaves out; later days keep their spacing), and
// every time in the answer is moved forward by S again. The orchestrator, the
// local-time "until" rules and the response all work in the dates the user
// asked for. The plan itself reflects today's chargers and conditions; the
// response says so with far_future.

// farFutureAfter is how far ahead a departure may be before it is shifted;
// it is the gateway layer's own limit (rivian.MaxDepartureAhead stays the
// backstop and is never exceeded).
const farFutureAfter = rivian.MaxDepartureAhead

// farFutureShift is the shift for a request that starts at start, and whether
// it applies. now is read once per request so every call shifts identically.
func farFutureShift(start, now time.Time) (time.Duration, bool) {
	if start.IsZero() || !start.After(now.Add(farFutureAfter)) {
		return 0, false
	}
	return start.Sub(now), true
}

// shiftedPlanner plans in shifted time (see above).
type shiftedPlanner struct {
	inner rivian.TripPlanner
	shift time.Duration
	now   time.Time
}

// PlanTrip implements rivian.TripPlanner.
func (p shiftedPlanner) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	if !in.DepartureTime.IsZero() {
		in.DepartureTime = in.DepartureTime.Add(-p.shift)
		if !in.DepartureTime.After(p.now) {
			in.DepartureTime = time.Time{} // "now": the gateway query omits it
		}
	}
	plan, err := p.inner.PlanTrip(ctx, in)
	if err != nil || plan == nil {
		return plan, err
	}
	return shiftTripPlan(plan, p.shift), nil
}

// shiftTripPlan returns a copy of plan with every waypoint time moved by d.
func shiftTripPlan(plan *rivian.TripPlan, d time.Duration) *rivian.TripPlan {
	move := func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
		return t.Add(d).UTC().Format(time.RFC3339)
	}
	out := *plan
	out.Routes = make([]rivian.TripRoute, len(plan.Routes))
	for i, r := range plan.Routes {
		wps := make([]rivian.PlannedWaypoint, len(r.Waypoints))
		for j, w := range r.Waypoints {
			w.ArrivalTimeUTC, w.DepartureTimeUTC = move(w.ArrivalTimeUTC), move(w.DepartureTimeUTC)
			wps[j] = w
		}
		r.Waypoints = wps
		out.Routes[i] = r
	}
	return &out
}
