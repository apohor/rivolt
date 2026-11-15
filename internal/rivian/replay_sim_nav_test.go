package rivian

import (
	"context"
	"testing"
	"time"
)

// simRun books a simulated run of kind in a feed and returns the feed with the run's window.
func simRun(t *testing.T, kind SimKind) (*ReplayFeed, SimRun) {
	t.Helper()
	start := simStartFor(t, kind)
	p, err := planSim(kind, start)
	if err != nil {
		t.Fatal(err)
	}
	run := SimRun{ID: "r", Kind: kind, Start: start, End: p.end, AddedMi: round2(p.addedMi)}
	return testSimFeed(&memSimStore{st: SimState{Runs: []SimRun{run}}}), run
}

func TestSimDriveNavCarriesTheErrandThenHome(t *testing.T) {
	f, run := simRun(t, SimDrive)
	ctx := context.Background()
	var dests []string
	var lastEnded bool
	seen := 0
	for at := run.Start; at.Before(run.End); at = at.Add(5 * time.Second) {
		fr := f.NavFramesAt(ctx, at)
		if len(fr) == 0 {
			continue // a parked stretch before or after the drive
		}
		seen++
		info, prog := fr[0].Info, fr[1].Progress
		if info == nil || prog == nil || info.Stops != 1 || !prog.HasLegDist || prog.NextStop != 0 {
			t.Fatalf("%s: info %+v progress %+v", at, info, prog)
		}
		if prog.LegMetres < 0 || prog.LegMetres > info.LegMetres[0] {
			t.Fatalf("%s: %.0f m left of a %.0f m leg", at, prog.LegMetres, info.LegMetres[0])
		}
		if len(dests) == 0 || dests[len(dests)-1] != info.Name {
			dests = append(dests, info.Name)
		}
		lastEnded = len(fr) == 3 && fr[2].Info != nil && fr[2].Info.Stops == 0
		if len(fr) == 3 && !lastEnded {
			t.Fatalf("%s: a third frame that does not end the trip", at)
		}
	}
	if seen == 0 || len(dests) != 2 || dests[0] != "Santa Anita Park" || dests[1] != "Rose Bowl Stadium" {
		t.Fatalf("saw %d frames, destinations in order: %v", seen, dests)
	}
	if !lastEnded {
		t.Fatal("the last frame of the drive must end the trip")
	}
}

func TestSimNavIsOnlyForADriveInsideItsWindow(t *testing.T) {
	ctx := context.Background()
	f, run := simRun(t, SimDrive)
	if f.NavFramesAt(ctx, run.Start.Add(-time.Minute)) != nil || f.NavFramesAt(ctx, run.End.Add(time.Minute)) != nil {
		t.Fatal("navigation outside the run")
	}
	c, crun := simRun(t, SimCharge)
	for at := crun.Start; at.Before(crun.End); at = at.Add(time.Minute) {
		if c.NavFramesAt(ctx, at) != nil {
			t.Fatalf("a charge carries navigation at %s", at)
		}
	}
	if testSimFeed(nil).NavFramesAt(ctx, time.Now()) != nil {
		t.Fatal("a feed with no sim store has navigation")
	}
}

func TestSimNavRouteIsPrecision6AndEndsAtTheDestination(t *testing.T) {
	f, run := simRun(t, SimDrive)
	var route string
	for at := run.Start; route == "" && at.Before(run.End); at = at.Add(5 * time.Second) {
		if fr := f.NavFramesAt(context.Background(), at); len(fr) > 0 {
			route = fr[0].Info.Route
		}
	}
	pts := decodePolyline6(route)
	if len(pts) < 2 {
		t.Fatalf("route has %d points", len(pts))
	}
	if d := haversineMeters(pts[len(pts)-1].Lat, pts[len(pts)-1].Lon, sampleErrand[0].Lat, sampleErrand[0].Lon); d > 5 {
		t.Fatalf("route ends %.0f m from the errand", d)
	}
}

func decodePolyline6(s string) []samplePoint {
	var out []samplePoint
	lat, lon := 0, 0
	next := func(i *int) int {
		res, shift := 0, 0
		for {
			b := int(s[*i]) - 63
			*i++
			res |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if res&1 != 0 {
			return ^(res >> 1)
		}
		return res >> 1
	}
	for i := 0; i < len(s); {
		lat += next(&i)
		lon += next(&i)
		out = append(out, samplePoint{float64(lat) / 1e6, float64(lon) / 1e6})
	}
	return out
}

type navFeedStub struct{ frames []*NavFrame }

func (navFeedStub) Stream(context.Context, string, func(State, bool)) error { return nil }
func (s navFeedStub) NavFramesAt(context.Context, string, time.Time) []*NavFrame {
	return s.frames
}

func TestReplayNavFollowsTheFlagAndSkipsBacklog(t *testing.T) {
	on := false
	mid := []*NavFrame{{Info: &NavInfo{Name: "X", Lat: 34, Lon: -118, Stops: 1, LegMetres: []float64{1000}}},
		{Progress: &NavProgress{ETA: time.Now().Add(time.Minute), LegMetres: 500, HasLegDist: true}}}
	m := NewSourceMonitor(navFeedStub{mid}, nil, nil)
	m.SetNavDecodeGate(func() bool { return on })
	ctx := context.Background()
	m.replayNav(ctx, "v", time.Now(), false)
	if _, ok := m.NavDestination("v"); ok {
		t.Fatal("a trip held with the flag off")
	}
	on = true
	m.replayNav(ctx, "v", time.Now(), true)
	if _, ok := m.NavDestination("v"); ok {
		t.Fatal("a backlog frame made a trip")
	}
	m.replayNav(ctx, "v", time.Now(), false)
	trip, ok := m.NavDestination("v")
	if !ok || trip.Name != "X" || !trip.HasDist || trip.MetresLeft != 500 {
		t.Fatalf("trip %+v ok %v", trip, ok)
	}
	m.source = navFeedStub{[]*NavFrame{{Info: &NavInfo{}}}}
	m.replayNav(ctx, "v", time.Now(), false)
	if _, ok := m.NavDestination("v"); ok {
		t.Fatal("the end frame left the trip up")
	}
}
