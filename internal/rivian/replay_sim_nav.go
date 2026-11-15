package rivian

// The navigation a simulated drive carries on a replay install (preview): the destination, the route and the trip progress the live
// feed would decode, so the Live map shows a pin, a route and an ETA with no Rivian behind it. The drive is the errand and back: out to
// the errand with the errand as the destination, then home with home as the destination, and the trip ends on the last frame.
//
// The frames are asked for by the moment (NavFrameSource), not carried on the State, and every one repeats its leg's trip_info (the store
// takes a repeat as a refresh), so a monitor that joins mid-leg still has the route. They are decoded values built here; nothing is encoded
// and decoded.

import (
	"context"
	"math"
	"strings"
	"time"
)

const metresPerMile = 1609.344

// encodePolyline6 is the polyline algorithm at precision 6, the precision of the route the live feed carries.
func encodePolyline6(pts []samplePoint) string {
	var b strings.Builder
	var prevLat, prevLon int32
	for _, p := range pts {
		lat := int32(math.Round(p.Lat * 1e6))
		lon := int32(math.Round(p.Lon * 1e6))
		encodeSigned(&b, lat-prevLat)
		encodeSigned(&b, lon-prevLon)
		prevLat, prevLon = lat, lon
	}
	return b.String()
}

// NavFramesAt returns the navigation frames of the simulated drive under way at wall time at, or nil when there is none.
func (f *ReplayFeed) NavFramesAt(ctx context.Context, at time.Time) []*NavFrame {
	if f.sim == nil {
		return nil
	}
	for _, r := range f.sim.get(ctx).Runs {
		if r.Kind != SimDrive || at.Before(r.Start) || !at.Before(r.End) {
			continue
		}
		p, err := planSim(r.Kind, r.Start)
		if err != nil {
			return nil
		}
		for _, seg := range p.segs {
			if seg.kind == segDrive {
				return simNavFrames(seg, p.shift, at)
			}
		}
	}
	return nil
}

// simNavFrames is the navigation of one simulated drive segment at wall time at. The turn at the errand is the frame nearest it.
func simNavFrames(seg sampleSeg, shift time.Duration, at time.Time) []*NavFrame {
	errand := sampleErrand[0]
	n := seg.frames()
	turn, best := 0, math.MaxFloat64
	for i := 0; i < n; i++ {
		fr := seg.frameAt(i)
		if d := haversineMeters(fr.Lat, fr.Lon, errand.Lat, errand.Lon); d < best {
			best, turn = d, i
		}
	}
	// The frame the moment falls in.
	i := int(at.Add(-shift).Sub(seg.start) / seg.step)
	i = max(0, min(i, n-1))
	fr := seg.frameAt(i)
	turnAt := seg.frameAt(turn).At.Add(shift)
	endAt := seg.frameAt(n - 1).At.Add(shift)

	name, to, arrive := "Santa Anita Park", errand, turnAt
	way := []samplePoint{SampleHome, errand}
	if i > turn {
		name, to, arrive = "Rose Bowl Stadium", SampleHome, endAt
		way = []samplePoint{errand, SampleHome}
	}
	path, mi := sampleRoad(way)
	metres := mi * metresPerMile
	info := &NavInfo{Name: name, Lat: to.Lat, Lon: to.Lon, Stops: 1, LegMetres: []float64{metres}, Route: encodePolyline6(path)}
	left := haversineMeters(fr.Lat, fr.Lon, to.Lat, to.Lon) * sampleRoadRatio
	out := []*NavFrame{{Info: info}, {Progress: &NavProgress{ETA: arrive, LegMetres: math.Min(left, metres), HasLegDist: true}}}
	if i == n-1 {
		out = append(out, &NavFrame{Info: &NavInfo{}}) // no stops: the trip is over
	}
	return out
}
