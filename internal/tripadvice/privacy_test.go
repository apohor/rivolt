package tripadvice

import (
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

// The prompt must carry no typed address and no full-precision coordinate:
// places are charger names or locations rounded to the weather grid.
func TestPromptHasNoAddressOrPreciseCoordinates(t *testing.T) {
	const addr = "1234 Elm Street, Reno NV 89501"
	plan := &rivian.TripPlan{Status: "OK", Routes: []rivian.TripRoute{{
		DestinationReached: true,
		Waypoints: []rivian.PlannedWaypoint{
			// The planner may echo the typed address as the waypoint name.
			{WaypointType: "ORIGIN", Name: addr, Latitude: 39.529632, Longitude: -119.813803},
			{WaypointType: "CHARGER", Name: "Electrify America Fernley", Latitude: 39.608712, Longitude: -119.251467, ArrivalSoC: 20, DepartureSoC: 80, ChargeDurationSec: 1500},
			{WaypointType: "CHARGER", Name: "", Latitude: 39.163458, Longitude: -119.766121, ArrivalSoC: 15, DepartureSoC: 60, ChargeDurationSec: 900},
			{WaypointType: "DESTINATION", Name: "987 Oak Ave, Carson City", Latitude: 38.987654, Longitude: -119.123456},
		},
	}}}
	_, prompt := buildPrompt(plan, Context{StartingSoC: 90, PackKWh: 135}, CostEstimate{})

	for _, forbidden := range []string{
		addr, "1234 Elm", "Reno NV", "987 Oak", "Carson City",
		"39.529632", "39.5296", "119.813803", "119.8138",
		"39.608712", "119.251467", "39.163458", "119.766121", "38.987654", "119.123456",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt contains %q:\n%s", forbidden, prompt)
		}
	}
	// What remains: charger names kept, places as rounded locations.
	for _, want := range []string{"Electrify America Fernley", "Origin: near 39.5, -119.8", "Destination: near 39.0, -119.1", "near 39.2, -119.8"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestEndpointLabelWithoutAWaypointIsUnknown(t *testing.T) {
	if got := endpointLabel(&rivian.TripPlan{}, "ORIGIN"); got != "unknown" {
		t.Errorf("got %q", got)
	}
}
