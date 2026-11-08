package forward_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// plan_legs forwards a rivian.PlanTripInput, field for field. This test fails when the planner's input gains a field the forwarded
// args neither mirror nor explicitly leave out, so the two cannot drift apart silently (a field that never crossed the wire would
// make a forwarded plan differ from a local one).
func TestPlanLegsArgsMirrorPlanTripInput(t *testing.T) {
	// planner input field -> the forwarded args field that carries it ("" with a reason: deliberately not forwarded)
	type m struct{ to, why string }
	cases := []struct {
		name   string
		from   reflect.Type
		to     reflect.Type
		fields map[string]m
	}{
		{"PlanTripInput", reflect.TypeOf(rivian.PlanTripInput{}), reflect.TypeOf(forward.PlanLegsArgs{}), map[string]m{
			"VehicleID": {"VehicleID", ""}, "StartingSoC": {"StartingSoC", ""}, "StartingRangeMeters": {"StartingRangeMeters", ""},
			"OriginBearing": {"OriginBearing", ""}, "Waypoints": {"Waypoints", ""}, "TargetArrivalSocPercent": {"TargetArrivalSocPercent", ""},
			"NetworkPreferences": {"NetworkPreferences", ""}, "DriveMode": {"DriveMode", ""}, "HasAdapter": {"HasAdapter", ""},
			"RoutePreferences": {"RoutePreferences", ""}, "DepartureTime": {"DepartureTimeMS", "a time.Time travels as unix milliseconds"},
			"TrailerProfile":          {"", "kept on the input for SPA compatibility; not sent to Rivian's planTrip schema"},
			"AvoidAdapterRequired":    {"", "kept on the input for SPA compatibility; not sent to Rivian's planTrip schema"},
			"SupportedConnectorTypes": {"", "kept on the input for SPA compatibility; not sent to Rivian's planTrip schema"},
		}},
		{"PlanTripWaypoint", reflect.TypeOf(rivian.PlanTripWaypoint{}), reflect.TypeOf(forward.Waypoint{}), map[string]m{
			"Latitude": {"Latitude", ""}, "Longitude": {"Longitude", ""}, "WaypointType": {"WaypointType", ""}, "EntityID": {"EntityID", ""},
		}},
		{"NetworkPreference", reflect.TypeOf(rivian.NetworkPreference{}), reflect.TypeOf(forward.NetworkPref{}), map[string]m{
			"NetworkID": {"NetworkID", ""}, "Preference": {"Preference", ""},
		}},
		{"RoutePreferences", reflect.TypeOf(rivian.RoutePreferences{}), reflect.TypeOf(forward.RoutePrefs{}), map[string]m{
			"AvoidTolls": {"AvoidTolls", ""}, "AvoidFerries": {"AvoidFerries", ""}, "AvoidHighways": {"AvoidHighways", ""},
		}},
	}
	for _, c := range cases {
		have := map[string]bool{}
		for i := 0; i < c.from.NumField(); i++ {
			have[c.from.Field(i).Name] = true
		}
		var names []string
		for n := range have {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			mm, ok := c.fields[n]
			if !ok {
				t.Errorf("rivian.%s has a field %q that forward.%s neither mirrors nor leaves out: add it to the args (and its validator) or list it here with the reason it is not forwarded", c.name, n, c.to.Name())
				continue
			}
			if mm.to == "" {
				if mm.why == "" {
					t.Errorf("rivian.%s.%s is left out without a reason", c.name, n)
				}
				continue
			}
			if _, ok := c.to.FieldByName(mm.to); !ok {
				t.Errorf("rivian.%s.%s should travel as forward.%s.%s, which does not exist", c.name, n, c.to.Name(), mm.to)
			}
		}
		for n := range c.fields {
			if !have[n] {
				t.Errorf("this test lists rivian.%s.%s, which no longer exists: remove it", c.name, n)
			}
		}
		// Every args field is accounted for: it is the target of some planner field.
		targets := map[string]bool{}
		for _, mm := range c.fields {
			if mm.to != "" {
				targets[mm.to] = true
			}
		}
		for i := 0; i < c.to.NumField(); i++ {
			if n := c.to.Field(i).Name; !targets[n] {
				t.Errorf("forward.%s.%s mirrors nothing in rivian.%s", c.to.Name(), n, c.name)
			}
		}
	}
}
