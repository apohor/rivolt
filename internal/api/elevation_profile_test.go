package api

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
)

func planOver(line [][2]float64, wps ...rivian.PlannedWaypoint) *rivian.TripPlan {
	return &rivian.TripPlan{Routes: []rivian.TripRoute{{Waypoints: wps, Geometry: maps.EncodePolyline6(line), GeometryPrecision: 6}}}
}

func profileJSON(t *testing.T, p *elevationProfileDTO) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A short trip below sea level: whole signed metres, every sample kept (fewer than the cap), stops at both ends of the line.
func TestElevationProfileShortTripBelowSeaLevelGolden(t *testing.T) {
	line := [][2]float64{{36.0, -97.0}, {36.004, -97.0}} // about 445 m: three samples (0, 250 m, end)
	dem := &fakeDEM{h: func(lat float64) float64 { return -86 + 20000*(lat-36.0) }}
	adj := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 1)}
	got := addLegClimb(withPlanElevation(context.Background(), dem), adj, planOver(line, wp(36.0), wp(36.004)))
	if got == nil {
		t.Fatal("no profile")
	}
	const want = `{"points":[[0,-86],[250,-41],[445,-6]],"waypoint_at_m":[0,445]}`
	if j := profileJSON(t, got); j != want {
		t.Errorf("golden\n got  %s\n want %s", j, want)
	}
}

// Over the cap: at most 200 points, the endpoints exact, the real summit and valley survive (not averaged away) and distances never go back.
func TestElevationProfileLongRouteKeepsExtremesAndEnds(t *testing.T) {
	line := lineNorth(30.0, 31.5) // about 167 km: about 670 samples
	summit, valley := 30.62, 30.97
	dem := &fakeDEM{h: func(lat float64) float64 {
		h := 300.0
		h += 40 * math.Sin(lat*30)
		h += 120 * math.Exp(-math.Pow((lat-summit)/0.004, 2)) // a narrow peak
		h -= 150 * math.Exp(-math.Pow((lat-valley)/0.004, 2)) // a narrow dip
		return h
	}}
	adj := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 2)}
	plan := planOver(line, wp(30.0), wp(30.75), wp(31.5))
	got := addLegClimb(withPlanElevation(context.Background(), dem), adj, plan)
	if got == nil {
		t.Fatal("no profile")
	}
	if n := len(got.Points); n > 200 || n < 150 {
		t.Fatalf("points %d, want at most 200 and close to it", n)
	}
	if got.Points[0][0] != 0 {
		t.Errorf("first point at %d m", got.Points[0][0])
	}
	total := int(math.Round(lineLengthM(line)))
	if last := got.Points[len(got.Points)-1][0]; last != total {
		t.Errorf("last point at %d m, want the line's end %d", last, total)
	}
	if got.WaypointAtM[0] == nil || *got.WaypointAtM[0] != 0 || got.WaypointAtM[2] == nil || *got.WaypointAtM[2] != total {
		t.Errorf("waypoint_at_m %v", got.WaypointAtM)
	}
	// The extremes of the full series are in the downsampled one.
	rc := routeClimb(withPlanElevation(context.Background(), dem), dem, maps.EncodePolyline6(line), 6, plan.Routes[0].Waypoints)
	var fullMax, fullMin = math.Inf(-1), math.Inf(1)
	for _, l := range rc.Legs {
		for _, h := range l.HeightsM {
			fullMax, fullMin = math.Max(fullMax, h), math.Min(fullMin, h)
		}
	}
	gotMax, gotMin := math.Inf(-1), math.Inf(1)
	for i, p := range got.Points {
		if i > 0 && p[0] < got.Points[i-1][0] {
			t.Fatalf("distance goes back at %d: %v", i, p)
		}
		gotMax, gotMin = math.Max(gotMax, float64(p[1])), math.Min(gotMin, float64(p[1]))
	}
	if gotMax != math.Round(fullMax) || gotMin != math.Round(fullMin) {
		t.Errorf("extremes %v..%v, want %v..%v", gotMin, gotMax, math.Round(fullMin), math.Round(fullMax))
	}
	// The same plan gives the same bytes every time.
	adj2 := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 2)}
	if a, b := profileJSON(t, got), profileJSON(t, addLegClimb(withPlanElevation(context.Background(), dem), adj2, plan)); a != b {
		t.Error("profile is not deterministic")
	}
}

// A stop that is not near the line is null, never a guessed distance; the others stay.
func TestElevationProfileUnplacedStopIsNull(t *testing.T) {
	line := lineNorth(30.0, 30.3)
	dem := &fakeDEM{h: func(lat float64) float64 { return 100 }}
	adj := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 2)}
	off := rivian.PlannedWaypoint{Latitude: 30.15, Longitude: -96.9} // about 9 km east of the line
	got := addLegClimb(withPlanElevation(context.Background(), dem), adj, planOver(line, wp(30.0), off, wp(30.3)))
	if got == nil {
		t.Fatal("no profile")
	}
	if len(got.WaypointAtM) != 3 || got.WaypointAtM[0] == nil || got.WaypointAtM[1] != nil || got.WaypointAtM[2] == nil {
		t.Errorf("waypoint_at_m %s", profileJSON(t, got))
	}
}

// Any unreadable leg removes the whole field (the climb figures of the readable legs stay), and weather off (no adjustment) reads no DEM.
func TestElevationProfileAbsentWhenIncompleteOrWeatherOff(t *testing.T) {
	line := lineNorth(30.0, 30.6)
	plan := planOver(line, wp(30.0), wp(30.3), wp(30.6))
	dem := &fakeDEM{h: func(lat float64) float64 { return 100 + 500*(lat-30.0) }, missingAbove: 30.4}
	adj := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 2)}
	if got := addLegClimb(withPlanElevation(context.Background(), dem), adj, plan); got != nil {
		t.Errorf("partial profile: %s", profileJSON(t, got))
	}
	if adj.Legs[0].ClimbM == nil || adj.Legs[1].ClimbM != nil {
		t.Error("per-leg climb behaviour changed")
	}
	off := &fakeDEM{h: func(float64) float64 { return 100 }}
	if got := addLegClimb(withPlanElevation(context.Background(), off), nil, plan); got != nil || off.reads != 0 {
		t.Errorf("weather off: profile %v, %d DEM reads, want none", got, off.reads)
	}
	// And in the response JSON the field is simply not there.
	b, _ := json.Marshal(tripPlanResponse{TripPlan: plan})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["elevation_profile"]; ok {
		t.Error("elevation_profile present without a profile")
	}
}

func TestDownsampleProfileEdgeCases(t *testing.T) {
	if downsampleProfile(nil, nil, 200) != nil {
		t.Error("empty series")
	}
	// All samples at one distance still yields at most max points and keeps both ends.
	n := 500
	d, h := make([]float64, n), make([]float64, n)
	for i := range h {
		h[i] = float64(i % 7)
	}
	if got := downsampleProfile(d, h, 200); len(got) > 200 || len(got) < 2 {
		t.Errorf("zero-span series gave %d points", len(got))
	}
}
