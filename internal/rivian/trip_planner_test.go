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

func TestBuildPlanTrip2Query_NetworkPreferencesVariableForm(t *testing.T) {
	in := PlanTripInput{
		VehicleID:   "v1",
		StartingSoC: 80,
		Waypoints: []PlanTripWaypoint{
			{Latitude: 32.7767, Longitude: -96.797, WaypointType: "origin"},
			{Latitude: 29.4241, Longitude: -98.4936, WaypointType: "destination"},
		},
		NetworkPreferences: []NetworkPreference{
			{NetworkID: "10027"}, // EA
			{NetworkID: ""},      // skipped (empty)
			{NetworkID: "10050"}, // Tesla
		},
	}
	q, vars := buildPlanTrip2Query(in)
	// Query header declares the variable; planTrip2 references it.
	if !strings.Contains(q, "$networkPreferences: [NetworkPreference!]") {
		t.Errorf("query missing $networkPreferences declaration:\n%s", q)
	}
	if !strings.Contains(q, "networkPreferences: $networkPreferences") {
		t.Errorf("planTrip2 args don't reference the variable:\n%s", q)
	}
	// Variables map carries the IDs (empty entry filtered out).
	prefs, ok := vars["networkPreferences"].([]map[string]any)
	if !ok {
		t.Fatalf("vars.networkPreferences wrong type: %T", vars["networkPreferences"])
	}
	if len(prefs) != 2 {
		t.Fatalf("want 2 prefs (empty entry filtered), got %d: %v", len(prefs), prefs)
	}
	if prefs[0]["networkId"] != "10027" || prefs[1]["networkId"] != "10050" {
		t.Errorf("preserve order: got %v", prefs)
	}
	// No inline literal — that was the form Rivian rejected.
	if strings.Contains(q, `{networkId:`) {
		t.Errorf("must not inline networkPreferences literal:\n%s", q)
	}

	// Empty input → no $networkPreferences anywhere, vars nil.
	in.NetworkPreferences = nil
	q, vars = buildPlanTrip2Query(in)
	if strings.Contains(q, "networkPreferences") {
		t.Errorf("with no prefs, query should not mention networkPreferences:\n%s", q)
	}
	if vars != nil {
		t.Errorf("with no prefs, vars should be nil, got %v", vars)
	}
}
