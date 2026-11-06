package rivian

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleLabelPlan() *TripPlan {
	return &TripPlan{Routes: []TripRoute{{Waypoints: []PlannedWaypoint{
		{WaypointType: "ORIGIN", Latitude: 34.0, Longitude: -118.0},
		{WaypointType: "DC_CHARGE_STATION", EntityID: "rivian-1", Name: "Planner pick", Latitude: 35.0, Longitude: -117.0},
		{WaypointType: "DC_CHARGE_STATION", EntityID: "ev-77", Name: "At a via", Latitude: 36.001, Longitude: -116.001},
		{WaypointType: "DESTINATION", Latitude: 37.0, Longitude: -115.0},
	}}}}
}

func TestLabelStopsSaysWhereEachStopCameFrom(t *testing.T) {
	vias := []ViaPoint{{Lat: 36.0, Lon: -116.0, EntityID: "poi-9"}, {Lat: 40.0, Lon: -100.0, EntityID: "far-away"}}
	out := LabelStops(sampleLabelPlan(), vias, false)
	ws := out.Routes[0].Waypoints
	want := []string{StopOrigin, StopPlanner, StopVia, StopDestination}
	for i, w := range ws {
		if w.Source != want[i] {
			t.Errorf("stop %d (%s): source %q, want %q", i, w.Name, w.Source, want[i])
		}
	}
	if ws[2].ViaIndex == nil || *ws[2].ViaIndex != 0 || ws[2].ViaEntityID != "poi-9" {
		t.Errorf("the via stop: index %v entity %q, want 0 and poi-9", ws[2].ViaIndex, ws[2].ViaEntityID)
	}
	if ws[1].ViaIndex != nil || ws[1].ViaEntityID != "" {
		t.Errorf("a planner pick carries via fields: %v %q", ws[1].ViaIndex, ws[1].ViaEntityID)
	}
	if ws[2].EntityID != "ev-77" {
		t.Error("the stop's own (Rivian) entity id must stay as it was")
	}
}

func TestLabelStopsPicksTheNearestViaAndHonoursTheLimit(t *testing.T) {
	plan := &TripPlan{Routes: []TripRoute{{Waypoints: []PlannedWaypoint{
		{WaypointType: "ORIGIN", Latitude: 34, Longitude: -118},
		{WaypointType: "DC_CHARGE_STATION", Latitude: 36.0, Longitude: -116.0},  // exactly on via 1
		{WaypointType: "DC_CHARGE_STATION", Latitude: 36.03, Longitude: -116.0}, // ~3.3 km from any via: a pick
		{WaypointType: "DESTINATION", Latitude: 37, Longitude: -115},
	}}}}
	out := LabelStops(plan, []ViaPoint{{Lat: 36.001, Lon: -116.0, EntityID: "a"}, {Lat: 36.0, Lon: -116.0, EntityID: "b"}}, false)
	ws := out.Routes[0].Waypoints
	if ws[1].Source != StopVia || *ws[1].ViaIndex != 1 || ws[1].ViaEntityID != "b" {
		t.Errorf("the nearest via should win: %+v", ws[1])
	}
	if ws[2].Source != StopPlanner {
		t.Errorf("a stop 3 km from every via is a planner pick: %+v", ws[2])
	}
}

func TestLabelStopsOvernightEndAndMissingTypes(t *testing.T) {
	out := LabelStops(sampleLabelPlan(), nil, true)
	ws := out.Routes[0].Waypoints
	if ws[3].Source != StopOvernight {
		t.Errorf("the day's end with an overnight: %q", ws[3].Source)
	}
	if ws[0].Source != StopOrigin || ws[1].Source != StopPlanner {
		t.Errorf("others unchanged: %q %q", ws[0].Source, ws[1].Source)
	}
	// stops with no type (older or replayed plans): first is the origin, last the destination
	untyped := &TripPlan{Routes: []TripRoute{{Waypoints: []PlannedWaypoint{{Latitude: 1}, {Latitude: 2}, {Latitude: 3}}}}}
	u := LabelStops(untyped, nil, false).Routes[0].Waypoints
	if u[0].Source != StopOrigin || u[1].Source != StopPlanner || u[2].Source != StopDestination {
		t.Errorf("untyped: %q %q %q", u[0].Source, u[1].Source, u[2].Source)
	}
}

// A plan can be shared (fixtures, replays): labelling works on a copy and never changes the input.
func TestLabelStopsLeavesTheInputAlone(t *testing.T) {
	in := sampleLabelPlan()
	out := LabelStops(in, []ViaPoint{{Lat: 36.0, Lon: -116.0}}, true)
	for _, w := range in.Routes[0].Waypoints {
		if w.Source != "" || w.ViaIndex != nil || w.ViaEntityID != "" {
			t.Fatalf("the input was labelled: %+v", w)
		}
	}
	if out == in || &out.Routes[0].Waypoints[0] == &in.Routes[0].Waypoints[0] {
		t.Error("the output shares its stops with the input")
	}
	if LabelStops(nil, nil, false) != nil {
		t.Error("nil must stay nil")
	}
}

func TestLabelStopsWireNames(t *testing.T) {
	b, err := json.Marshal(LabelStops(sampleLabelPlan(), []ViaPoint{{Lat: 36.0, Lon: -116.0, EntityID: "poi-9"}}, false))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"Source":"via"`, `"ViaIndex":0`, `"ViaEntityID":"poi-9"`, `"Source":"planner"`, `"Source":"origin"`, `"Source":"destination"`} {
		if !strings.Contains(s, key) {
			t.Errorf("the wire is missing %s", key)
		}
	}
	// a plan that was never labelled (stored or old) serialises exactly as before
	plain, _ := json.Marshal(sampleLabelPlan())
	if strings.Contains(string(plain), "Source") || strings.Contains(string(plain), "ViaIndex") {
		t.Errorf("unlabelled plan grew keys: %s", plain)
	}
}
