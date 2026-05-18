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

// TestBuildPlanTrip2Query_NetworkPreferencesShape pins the wire shape
// the Apollo adapter in the official app produces: variable form with
// {networkId, preference: 1} per entry. Empty preference list = no
// $variable declared at all (don't even mention the field).
func TestBuildPlanTrip2Query_NetworkPreferencesShape(t *testing.T) {
	in := PlanTripInput{
		VehicleID:   "v1",
		StartingSoC: 80,
		Waypoints: []PlanTripWaypoint{
			{Latitude: 32.7767, Longitude: -96.797, WaypointType: "origin"},
			{Latitude: 29.4241, Longitude: -98.4936, WaypointType: "destination"},
		},
		NetworkPreferences: []NetworkPreference{
			{NetworkID: "10027"},
			{NetworkID: ""}, // skipped
			{NetworkID: "10050"},
		},
	}
	q, vars := buildPlanTrip2Query(in)
	if !strings.Contains(q, "$networkPreferences: [NetworkPreference!]") {
		t.Errorf("query missing $networkPreferences declaration:\n%s", q)
	}
	if !strings.Contains(q, "networkPreferences: $networkPreferences") {
		t.Errorf("planTrip2 args don't reference the variable:\n%s", q)
	}
	prefs, ok := vars["networkPreferences"].([]map[string]any)
	if !ok {
		t.Fatalf("vars.networkPreferences wrong type: %T", vars["networkPreferences"])
	}
	if len(prefs) != 2 {
		t.Fatalf("want 2 entries (empty filtered), got %d", len(prefs))
	}
	for i, p := range prefs {
		if p["networkId"] == "" {
			t.Errorf("entry %d missing networkId: %v", i, p)
		}
		if p["preference"] != 1 {
			t.Errorf("entry %d preference: want 1, got %v", i, p["preference"])
		}
	}

	in.NetworkPreferences = nil
	q, vars = buildPlanTrip2Query(in)
	if strings.Contains(q, "networkPreferences") {
		t.Errorf("with no prefs, query must not mention networkPreferences:\n%s", q)
	}
	if vars != nil {
		t.Errorf("with no prefs, vars must be nil; got %v", vars)
	}
}
