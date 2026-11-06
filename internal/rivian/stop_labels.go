package rivian

import "strings"

// Where a returned stop came from. The planner's response lists origin, destination and its own charging picks; a via the user asked for
// is told apart from a pick by where it is, because only coordinates go upstream (buildPlanTrip2Query), never a type or an entity id.
const (
	StopOrigin      = "origin"
	StopDestination = "destination"
	StopVia         = "via"       // the stop is at (within ViaMatchM of) a via the request named
	StopPlanner     = "planner"   // a charging stop the planner chose
	StopOvernight   = "overnight" // a multi-day day's end that is an overnight stay
)

// ViaMatchM is how close a returned stop must be to a requested via to be that via: the planner snaps a point to its own place.
const ViaMatchM = 250.0

// ViaPoint is a via of the request: its position, the entity id the caller attached (echoed back), and its index among the vias.
type ViaPoint struct {
	Lat, Lon float64
	EntityID string
}

// LabelStops is a copy of the plan whose stops say where they came from: Source on every stop and, for a via, ViaIndex (the 0-based index among
// the request's vias) and ViaEntityID (what the caller sent). overnightEnd marks the destination of a multi-day day that ends in an overnight stay
// as StopOvernight. The input is never changed: a plan can be shared (fixtures, replays), so the stops are copied.
func LabelStops(plan *TripPlan, vias []ViaPoint, overnightEnd bool) *TripPlan {
	if plan == nil {
		return nil
	}
	out := *plan
	out.Routes = make([]TripRoute, len(plan.Routes))
	for i, r := range plan.Routes {
		r.Waypoints = append([]PlannedWaypoint(nil), r.Waypoints...)
		last := len(r.Waypoints) - 1
		for j := range r.Waypoints {
			w := &r.Waypoints[j]
			t := strings.ToLower(w.WaypointType)
			switch {
			case t == "origin" || (t == "" && j == 0):
				w.Source = StopOrigin
			case t == "destination" || (t == "" && j == last && j > 0):
				w.Source = StopDestination
				if overnightEnd {
					w.Source = StopOvernight
				}
			default:
				w.Source = StopPlanner
				if k := nearestVia(vias, w.Latitude, w.Longitude); k >= 0 {
					idx := k
					w.Source, w.ViaIndex, w.ViaEntityID = StopVia, &idx, vias[k].EntityID
				}
			}
		}
		out.Routes[i] = r
	}
	return &out
}

// nearestVia is the index of the closest via within ViaMatchM, or -1.
func nearestVia(vias []ViaPoint, lat, lon float64) int {
	best, bestM := -1, ViaMatchM
	for k, v := range vias {
		if d := haversineMeters(v.Lat, v.Lon, lat, lon); d <= bestM {
			best, bestM = k, d
		}
	}
	return best
}
