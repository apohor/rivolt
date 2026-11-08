package api

import (
	"context"
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
)

// fakeDEM answers a height from the latitude; missingAbove makes every point north of it unreadable.
type fakeDEM struct {
	h            func(lat float64) float64
	missingAbove float64
	reads        int
}

func (f *fakeDEM) LookupBlocking(_ context.Context, lat, _ float64) (float64, bool) {
	f.reads++
	if f.missingAbove != 0 && lat > f.missingAbove {
		return 0, false
	}
	return f.h(lat), true
}

// A straight north-south line of 0.01 degree steps (about 1.1 km), from lat0 to lat1.
func lineNorth(lat0, lat1 float64) [][2]float64 {
	var out [][2]float64
	for lat := lat0; lat <= lat1+1e-9; lat += 0.01 {
		out = append(out, [2]float64{lat, -97.0})
	}
	return out
}

func wp(lat float64) rivian.PlannedWaypoint {
	return rivian.PlannedWaypoint{Latitude: lat, Longitude: -97.0}
}

// Two legs: the first climbs 300 m and falls 100 m on a smooth ridge, the second is flat with sub-hysteresis jitter. The numbers come out
// within a few metres, split at the stop, and the jitter adds nothing.
func TestRouteLegClimbSplitsAtStopsAndIgnoresJitter(t *testing.T) {
	line := lineNorth(30.0, 30.6)
	geom := maps.EncodePolyline6(line)
	dem := &fakeDEM{h: func(lat float64) float64 {
		switch {
		case lat <= 30.2: // climbs 300 m over the first 0.2 degree
			return 100 + 300*(lat-30.0)/0.2
		case lat <= 30.3: // falls 100 m
			return 400 - 100*(lat-30.2)/0.1
		default: // flat at 300 with a 1.5 m wobble
			return 300 + 1.5*math.Sin(lat*4000)
		}
	}}
	got := routeLegClimb(context.Background(), dem, geom, 6, []rivian.PlannedWaypoint{wp(30.0), wp(30.3), wp(30.6)})
	if len(got) != 2 || !got[0].OK || !got[1].OK {
		t.Fatalf("legs: %+v", got)
	}
	if math.Abs(got[0].ClimbM-300) > 10 || math.Abs(got[0].DescentM-100) > 10 {
		t.Errorf("leg 0 climb %.1f descent %.1f, want about 300 and 100", got[0].ClimbM, got[0].DescentM)
	}
	if got[1].ClimbM != 0 || got[1].DescentM != 0 {
		t.Errorf("flat leg with a 1.5 m wobble: climb %.1f descent %.1f, want 0", got[1].ClimbM, got[1].DescentM)
	}
}

// A height that cannot be read fails that leg only, never a guess; the legs before it keep their figures.
func TestRouteLegClimbUnreadableLegHasNoFigures(t *testing.T) {
	line := lineNorth(30.0, 30.6)
	dem := &fakeDEM{h: func(lat float64) float64 { return 100 + 500*(lat-30.0) }, missingAbove: 30.35}
	got := routeLegClimb(context.Background(), dem, maps.EncodePolyline6(line), 6, []rivian.PlannedWaypoint{wp(30.0), wp(30.3), wp(30.6)})
	if len(got) != 2 || !got[0].OK || got[1].OK {
		t.Fatalf("legs: %+v", got)
	}
}

// No line, the wrong precision, no DEM or fewer than two stops: nothing.
func TestRouteLegClimbNeedsALine(t *testing.T) {
	dem := &fakeDEM{h: func(float64) float64 { return 0 }}
	g := maps.EncodePolyline6(lineNorth(30.0, 30.2))
	for name, got := range map[string][]legClimb{
		"no geometry": routeLegClimb(context.Background(), dem, "", 6, []rivian.PlannedWaypoint{wp(30), wp(30.2)}),
		"precision 5": routeLegClimb(context.Background(), dem, g, 5, []rivian.PlannedWaypoint{wp(30), wp(30.2)}),
		"no dem":      routeLegClimb(context.Background(), nil, g, 6, []rivian.PlannedWaypoint{wp(30), wp(30.2)}),
		"one stop":    routeLegClimb(context.Background(), dem, g, 6, []rivian.PlannedWaypoint{wp(30)}),
	} {
		if got != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// addLegClimb fills the adjustment's legs, rounded to whole metres, and leaves them alone when the request has no DEM.
func TestAddLegClimbFillsTheAdjustment(t *testing.T) {
	line := lineNorth(30.0, 30.2)
	plan := &rivian.TripPlan{Routes: []rivian.TripRoute{{
		Waypoints: []rivian.PlannedWaypoint{wp(30.0), wp(30.2)}, Geometry: maps.EncodePolyline6(line), GeometryPrecision: 6,
	}}}
	adj := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 1)}
	addLegClimb(context.Background(), adj, plan) // no DEM on the context
	if adj.Legs[0].ClimbM != nil {
		t.Fatal("climb filled without a DEM")
	}
	ctx := withPlanElevation(context.Background(), &fakeDEM{h: func(lat float64) float64 { return 100 + 1000*(lat-30.0) }})
	addLegClimb(ctx, adj, plan)
	if c := adj.Legs[0].ClimbM; c == nil || math.Abs(*c-200) > 8 || *c != math.Round(*c) {
		t.Errorf("climb %v, want about 200 whole metres", c)
	}
	if d := adj.Legs[0].DescentM; d == nil || *d != 0 {
		t.Errorf("descent %v, want 0", d)
	}
	// A leg count that does not match the adjustment's is ignored rather than misassigned.
	adj2 := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 3)}
	addLegClimb(ctx, adj2, plan)
	if adj2.Legs[0].ClimbM != nil {
		t.Error("figures assigned to a mismatched leg list")
	}
}
