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
	q := buildPlanTrip2Query(in)
	for _, want := range []string{`driveMode: CONSERVE`, `hasAdapter: true`} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q\nfull query:\n%s", want, q)
		}
	}
	// driveMode is a GraphQL enum, not a String — the gateway
	// rejects driveMode: "CONSERVE" with GRAPHQL_VALIDATION_FAILED.
	// See the v0.17.136 → v0.17.137 fix.
	if strings.Contains(q, `driveMode: "`) {
		t.Errorf("driveMode must be an unquoted enum literal, got:\n%s", q)
	}

	in.DriveMode = ""
	in.HasAdapter = nil
	q = buildPlanTrip2Query(in)
	for _, bad := range []string{`driveMode`, `hasAdapter`} {
		if strings.Contains(q, bad) {
			t.Errorf("unset planner should not emit %q, got:\n%s", bad, q)
		}
	}

	f := false
	in.HasAdapter = &f
	q = buildPlanTrip2Query(in)
	if !strings.Contains(q, `hasAdapter: false`) {
		t.Errorf("explicit false should emit hasAdapter: false, got:\n%s", q)
	}
}
