package api

import (
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestFirstLegBearing(t *testing.T) {
	plan := func(wps ...rivian.PlannedWaypoint) *rivian.TripPlan {
		return &rivian.TripPlan{Routes: []rivian.TripRoute{{Waypoints: wps}}}
	}
	// Due south: a north wind is then a tailwind.
	b, ok := firstLegBearing(plan(
		rivian.PlannedWaypoint{WaypointType: "ORIGIN", Latitude: 34, Longitude: -118},
		rivian.PlannedWaypoint{WaypointType: "CHARGER", Latitude: 33, Longitude: -118},
		rivian.PlannedWaypoint{WaypointType: "DESTINATION", Latitude: 32.7, Longitude: -117.1},
	))
	if !ok || math.Abs(b-180) > 0.5 {
		t.Errorf("got %.1f %v, want 180", b, ok)
	}
	// No charger: the destination is the first leg's end; a waypoint without a position is skipped.
	b, ok = firstLegBearing(plan(
		rivian.PlannedWaypoint{WaypointType: "ORIGIN", Latitude: 34, Longitude: -118},
		rivian.PlannedWaypoint{WaypointType: "CHARGER"},
		rivian.PlannedWaypoint{WaypointType: "DESTINATION", Latitude: 34, Longitude: -117},
	))
	if !ok || math.Abs(b-90) > 1 {
		t.Errorf("got %.1f %v, want about 90", b, ok)
	}
	if _, ok := firstLegBearing(plan(rivian.PlannedWaypoint{WaypointType: "ORIGIN", Latitude: 34, Longitude: -118})); ok {
		t.Error("origin alone has no bearing")
	}
	if _, ok := firstLegBearing(nil); ok {
		t.Error("nil plan has no bearing")
	}
}
