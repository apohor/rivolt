package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/radar"
	"github.com/apohor/rivolt/internal/rivian"
)

// fakeRadar answers from a function of latitude and time; ok=false is "cannot say".
type fakeRadar struct {
	at    func(lat float64, t time.Time) (radar.Level, bool)
	calls int
}

func (f *fakeRadar) At(_ context.Context, lat, _ float64, t time.Time) (radar.Reading, bool) {
	f.calls++
	l, ok := f.at(lat, t)
	return radar.Reading{Level: l}, ok
}

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func timedWP(lat float64, arrive, depart time.Duration) rivian.PlannedWaypoint {
	w := wp(lat)
	w.ArrivalTimeUTC = t0.Add(arrive).Format(time.RFC3339)
	w.DepartureTimeUTC = t0.Add(depart).Format(time.RFC3339)
	return w
}

// Rain falls only on the second half of the route and only after 14:00: the first leg is dry at the times the truck is on it, the second leg
// gets rain, and the time used is the truck's time at the point, not the departure time.
func TestRouteLegRadarUsesTimeAtThePoint(t *testing.T) {
	line := lineNorth(30.0, 30.6)
	fr := &fakeRadar{at: func(lat float64, tm time.Time) (radar.Level, bool) {
		if lat > 30.3 && tm.After(t0.Add(2*time.Hour)) {
			return radar.Rain, true
		}
		return radar.Dry, true
	}}
	wps := []rivian.PlannedWaypoint{timedWP(30.0, 0, 0), timedWP(30.3, time.Hour, time.Hour+10*time.Minute), timedWP(30.6, 3*time.Hour, 3*time.Hour)}
	got := routeLegRadar(context.Background(), fr, maps.EncodePolyline6(line), 6, wps)
	if len(got) != 2 || got[0] != "dry" || got[1] != "rain" {
		t.Fatalf("legs: %q", got)
	}
}

func TestRouteLegRadarSnowBeatsRain(t *testing.T) {
	line := lineNorth(30.0, 30.2)
	fr := &fakeRadar{at: func(lat float64, _ time.Time) (radar.Level, bool) {
		if lat > 30.15 {
			return radar.Snow, true
		}
		return radar.Rain, true
	}}
	got := routeLegRadar(context.Background(), fr, maps.EncodePolyline6(line), 6, []rivian.PlannedWaypoint{timedWP(30.0, 0, 0), timedWP(30.2, time.Hour, time.Hour)})
	if len(got) != 1 || got[0] != "snow" {
		t.Fatalf("legs: %q", got)
	}
}

// A leg the forecast cannot fully read is absent, never "dry": under 80% of its points answered gives no tag.
func TestRouteLegRadarPartialCoverageIsAbsent(t *testing.T) {
	line := lineNorth(30.0, 30.6)
	fr := &fakeRadar{at: func(lat float64, _ time.Time) (radar.Level, bool) {
		if lat > 30.4 { // the last third of the second leg is outside the forecast
			return "", false
		}
		return radar.Dry, true
	}}
	wps := []rivian.PlannedWaypoint{timedWP(30.0, 0, 0), timedWP(30.3, time.Hour, time.Hour), timedWP(30.6, 2*time.Hour, 2*time.Hour)}
	got := routeLegRadar(context.Background(), fr, maps.EncodePolyline6(line), 6, wps)
	if len(got) != 2 || got[0] != "dry" || got[1] != "" {
		t.Fatalf("legs: %q", got)
	}
}

func TestRouteLegRadarNeedsLineAndTimes(t *testing.T) {
	fr := &fakeRadar{at: func(float64, time.Time) (radar.Level, bool) { return radar.Rain, true }}
	g := maps.EncodePolyline6(lineNorth(30.0, 30.2))
	timed := []rivian.PlannedWaypoint{timedWP(30.0, 0, 0), timedWP(30.2, time.Hour, time.Hour)}
	if got := routeLegRadar(context.Background(), fr, "", 6, timed); got != nil {
		t.Errorf("no geometry: %q", got)
	}
	if got := routeLegRadar(context.Background(), fr, g, 5, timed); got != nil {
		t.Errorf("precision 5: %q", got)
	}
	if got := routeLegRadar(context.Background(), fr, g, 6, []rivian.PlannedWaypoint{wp(30.0), wp(30.2)}); len(got) != 1 || got[0] != "" {
		t.Errorf("no times: %q", got)
	}
	back := []rivian.PlannedWaypoint{timedWP(30.0, time.Hour, time.Hour), timedWP(30.2, 0, 0)}
	if got := routeLegRadar(context.Background(), fr, g, 6, back); len(got) != 1 || got[0] != "" {
		t.Errorf("arrival before departure: %q", got)
	}
}

func TestAddLegRadarFillsOnlyWithARadar(t *testing.T) {
	plan := &rivian.TripPlan{Routes: []rivian.TripRoute{{
		Waypoints: []rivian.PlannedWaypoint{timedWP(30.0, 0, 0), timedWP(30.2, time.Hour, time.Hour)},
		Geometry:  maps.EncodePolyline6(lineNorth(30.0, 30.2)), GeometryPrecision: 6,
	}}}
	adj := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 1)}
	addLegRadar(context.Background(), adj, plan)
	if adj.Legs[0].Radar != nil {
		t.Fatal("radar filled without a source")
	}
	ctx := withPlanRadar(context.Background(), &fakeRadar{at: func(float64, time.Time) (radar.Level, bool) { return radar.Rain, true }})
	addLegRadar(ctx, adj, plan)
	if r := adj.Legs[0].Radar; r == nil || r.Level != "rain" {
		t.Fatalf("radar: %+v", r)
	}
	adj2 := &tripWeatherAdjustmentDTO{Legs: make([]tripWeatherAdjLegDTO, 3)}
	addLegRadar(ctx, adj2, plan)
	if adj2.Legs[0].Radar != nil {
		t.Error("tag assigned to a mismatched leg list")
	}
}
