package api

import (
	"context"
	"math"
	"time"

	"github.com/apohor/rivolt/internal/maps"
)

// pathRouter is the part of the self-hosted Valhalla client the overnight search uses to put a sparse path on the road.
type pathRouter interface {
	Enabled() bool
	Route(ctx context.Context, locs [][2]float64, opts maps.RouteOpts) (maps.Route, error)
}

const (
	// densifyChordKm is the longest straight stretch left between two path points. A planner's waypoints (origin, a few chargers,
	// destination) can be hundreds of km apart; on a straight line between them the day-end point and the 12 km corridor can sit tens
	// of km off the road, so the search finds no stay that is on it.
	densifyChordKm = 30.0
	// densifyMaxRuns bounds the Valhalla calls one request can cause.
	densifyMaxRuns = 6
	// densifyMaxLocs is how many points one Valhalla call is given (its default limit is 50).
	densifyMaxLocs = 50
	// densifyTimeout bounds all the calls together; a path that cannot be routed in time is searched as it came.
	densifyTimeout = 10 * time.Second
)

// densifyPath replaces every straight stretch longer than densifyChordKm by the road between its ends, routed through our own
// Valhalla, and reports whether it changed anything. It is best effort: with no router, an unroutable stretch, an error or a timeout
// the stretch is kept as it came. Coordinates it would send are checked first; the search itself rejects an invalid path.
func densifyPath(ctx context.Context, r pathRouter, path [][2]float64) ([][2]float64, bool) {
	if r == nil || !r.Enabled() || len(path) < 2 {
		return path, false
	}
	for _, p := range path {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) || p[0] < -90 || p[0] > 90 || p[1] < -180 || p[1] > 180 {
			return path, false
		}
	}
	long := func(i int) bool { return chordKm(path[i], path[i+1]) > densifyChordKm } // the stretch from point i to i+1
	var out [][2]float64
	changed, runs := false, 0
	ctx, cancel := context.WithTimeout(ctx, densifyTimeout)
	defer cancel()
	for i := 0; i < len(path); {
		if i+1 >= len(path) || !long(i) || runs >= densifyMaxRuns {
			out = append(out, path[i])
			i++
			continue
		}
		// A run of consecutive long stretches from point i to point j.
		j := i + 1
		for j+1 < len(path) && long(j) && j-i < densifyMaxLocs-1 {
			j++
		}
		runs++
		route, err := r.Route(ctx, path[i:j+1], maps.RouteOpts{})
		if err != nil || len(route.Legs) != j-i {
			out = append(out, path[i:j+1]...) // keep the chords of this run
			i = j + 1
			continue
		}
		for k, leg := range route.Legs {
			shape := leg.Shape
			if k > 0 && len(shape) > 0 {
				shape = shape[1:] // each leg starts where the one before ended
			}
			out = append(out, shape...)
		}
		changed = true
		i = j + 1
	}
	return out, changed
}

// chordKm is the great-circle distance between two (lat, lon) points in km.
func chordKm(a, b [2]float64) float64 {
	const r = math.Pi / 180
	dl, dn := (b[0]-a[0])*r, (b[1]-a[1])*r
	h := math.Sin(dl/2)*math.Sin(dl/2) + math.Cos(a[0]*r)*math.Cos(b[0]*r)*math.Sin(dn/2)*math.Sin(dn/2)
	return 2 * 6371 * math.Asin(math.Sqrt(h))
}
