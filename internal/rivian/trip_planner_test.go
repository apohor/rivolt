package rivian

import (
	"strings"
	"testing"
)

func TestBuildPlanTrip2QueryWithDriveModeAndAdapter(t *testing.T) {
	tt := true
	in := PlanTripInput{
		VehicleID:   "v1",
		StartingSoC: 80,
		Waypoints: []PlanTripWaypoint{
			{Latitude: 32.7767, Longitude: -96.797, WaypointType: "origin"},
			{Latitude: 29.4241, Longitude: -98.4936, WaypointType: "destination"},
		},
		DriveMode:  "CONSERVE",
		HasAdapter: &tt,
	}
	q, _ := buildPlanTrip2Query(in)
	for _, want := range []string{`driveMode: CONSERVE`, `hasAdapter: true`} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q\nfull query:\n%s", want, q)
		}
	}
	if strings.Contains(q, `driveMode: "`) {
		t.Errorf("driveMode must be an unquoted enum literal, got:\n%s", q)
	}

	in.DriveMode = ""
	in.HasAdapter = nil
	q, _ = buildPlanTrip2Query(in)
	for _, bad := range []string{"driveMode", "hasAdapter"} {
		if strings.Contains(q, bad) {
			t.Errorf("unset planner should not emit %q, got:\n%s", bad, q)
		}
	}

	f := false
	in.HasAdapter = &f
	q, _ = buildPlanTrip2Query(in)
	if !strings.Contains(q, `hasAdapter: false`) {
		t.Errorf("explicit false should emit hasAdapter: false, got:\n%s", q)
	}
}

// TestBuildPlanTrip2Query_NetworkPreferencesParked guards the current
// no-op behaviour: even when the user has Preferred toggles set, no
// networkPreferences arg / variable / declaration leaks into the
// outgoing query. Two prior wire shapes were rejected by Rivian's
// gateway; until we have the persisted-query hash from the app,
// emitting *anything* on this field re-introduces the 502.
func TestBuildPlanTrip2Query_NetworkPreferencesParked(t *testing.T) {
	in := PlanTripInput{
		VehicleID:   "v1",
		StartingSoC: 80,
		Waypoints: []PlanTripWaypoint{
			{Latitude: 32.7767, Longitude: -96.797, WaypointType: "origin"},
			{Latitude: 29.4241, Longitude: -98.4936, WaypointType: "destination"},
		},
		NetworkPreferences: []NetworkPreference{
			{NetworkID: "10027"},
			{NetworkID: "10050"},
		},
	}
	q, vars := buildPlanTrip2Query(in)
	if strings.Contains(q, "networkPreferences") {
		t.Errorf("networkPreferences must not appear in the query (parked):\n%s", q)
	}
	if strings.Contains(q, "$networkPreferences") {
		t.Errorf("no $networkPreferences variable should be declared:\n%s", q)
	}
	if vars != nil {
		t.Errorf("variables must be nil; got %v", vars)
	}
}
