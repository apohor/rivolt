package api

import (
	"context"
	"math"
	"time"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
)

// ElevationBlocking is the self-hosted DEM as the planner reads it along a route: the terrain height at a point, waiting (up to ctx) for a
// missing tile. *elevation.Resolver satisfies it.
type ElevationBlocking interface {
	LookupBlocking(ctx context.Context, lat, lon float64) (float64, bool)
}

const (
	// climbStepM is the spacing of the heights sampled along a leg (the DEM's pixels are about 38 m at the equator; a kilometre-scale
	// smoothing below removes their noise).
	climbStepM = 250.0
	// climbMaxSamples bounds the DEM reads for one route: a longer route is sampled at a wider step.
	climbMaxSamples = 6000
	// climbSmoothWindow is the moving-average width (in samples) applied to the heights before they are summed.
	climbSmoothWindow = 5
	// climbHysteresisM: a rise or fall counts only once it exceeds this many metres from the last counted height, so DEM jitter on flat
	// ground sums to nothing.
	climbHysteresisM = 3.0
	// stopMatchM: the first vertex of the line within this distance of a stop is where that stop sits on the line.
	stopMatchM = 200.0
)

// climbBudget bounds the DEM reads for one plan response (all days of a multi-day plan together): a leg not read in time simply has no figures.
const climbBudget = 2500 * time.Millisecond

// withClimbBudget is the one deadline for the climb work of a response.
func withClimbBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, climbBudget)
}

// addLegClimb fills ClimbM and DescentM of each leg of adj from the DEM along the plan's first route (the one adj describes). It does
// nothing without an adjustment, a DEM on the request, or a road line, and never fails the plan. It has no deadline of its own: the caller
// puts ONE on ctx for the whole response (withClimbBudget), so a multi-day plan shares a single budget across its days.
//
// It returns the elevation profile built from the same DEM reads (the raw heights, not the smoothed copy the figures are summed from), or nil when any leg could not be read (the chart is never drawn from
// a partial series). It runs only when the climb pass runs, so a plan without an adjustment (weather off) reads no DEM and has no profile.
func addLegClimb(ctx context.Context, adj *tripWeatherAdjustmentDTO, plan *rivian.TripPlan) *elevationProfileDTO {
	el, _ := ctx.Value(planElevationKey{}).(ElevationBlocking)
	if adj == nil || el == nil || plan == nil || len(plan.Routes) == 0 {
		return nil
	}
	defer observePlanStage(ctx, "climb", time.Now())
	r := plan.Routes[0]
	rc := routeClimb(ctx, el, r.Geometry, r.GeometryPrecision, r.Waypoints)
	if len(rc.Legs) != len(adj.Legs) {
		return nil
	}
	for i, g := range rc.Legs {
		if !g.OK {
			continue
		}
		c, d := math.Round(g.ClimbM), math.Round(g.DescentM)
		adj.Legs[i].ClimbM, adj.Legs[i].DescentM = &c, &d
	}
	return rc.profile()
}

// legClimb is the climb and descent of one leg in metres; OK is false when the leg could not be read from the DEM.
type legClimb struct {
	ClimbM, DescentM float64
	OK               bool
	// DistM and HeightsM are the raw DEM heights read along the leg and where along the route's line (metres from its start) each was
	// read. They feed the elevation profile: unsmoothed, so a summit keeps its height and a short leg keeps its slope (the climb figures
	// are summed from the smoothed copy).
	DistM, HeightsM []float64
}

// routeClimbResult is what one pass over the route yields: the legs, and where each waypoint sits along the line (nil: not placed).
type routeClimbResult struct {
	Legs    []legClimb
	StopAtM []*float64
}

// routeLegClimb splits the route's road line (polyline6) at its stops and returns, for each leg between consecutive waypoints, the total
// climb and descent along it from the DEM. A leg whose line, stops or heights are unavailable has OK=false; nothing is guessed. The result
// has len(wps)-1 entries, or is nil when the route has no usable line.
func routeLegClimb(ctx context.Context, el ElevationBlocking, geometry string, precision int, wps []rivian.PlannedWaypoint) []legClimb {
	return routeClimb(ctx, el, geometry, precision, wps).Legs
}

func routeClimb(ctx context.Context, el ElevationBlocking, geometry string, precision int, wps []rivian.PlannedWaypoint) routeClimbResult {
	if el == nil || geometry == "" || precision != 6 || len(wps) < 2 {
		return routeClimbResult{}
	}
	line := maps.DecodePolyline6(geometry)
	if len(line) < 2 {
		return routeClimbResult{}
	}
	cum := make([]float64, len(line))
	for j := 1; j < len(line); j++ {
		cum[j] = cum[j-1] + distM(line[j-1][0], line[j-1][1], line[j][0], line[j][1])
	}
	// Where each stop sits on the line: moving forward only, the first vertex within stopMatchM, else the nearest one. The last stop is
	// the end of the line.
	at := make([]int, len(wps))
	for i := 1; i < len(wps); i++ {
		if i == len(wps)-1 {
			at[i] = len(line) - 1
			break
		}
		at[i] = matchStop(line, at[i-1], wps[i].Latitude, wps[i].Longitude)
	}
	// The first and last waypoints are the ends of the line by definition; a stop between is placed only when the matched vertex is within
	// stopMatchM of it (the legs still split at the nearest vertex, as before).
	stopAt := make([]*float64, len(wps))
	for i := range wps {
		d := cum[at[i]]
		if i > 0 && i < len(wps)-1 && distM(line[at[i]][0], line[at[i]][1], wps[i].Latitude, wps[i].Longitude) > stopMatchM {
			continue
		}
		stopAt[i] = &d
	}
	step := climbStepM
	if total := cum[len(cum)-1]; total/step > climbMaxSamples {
		step = total / climbMaxSamples
	}
	out := make([]legClimb, len(wps)-1)
	for i := range out {
		if at[i+1] <= at[i] {
			continue
		}
		heights, ok := legHeights(ctx, el, line[at[i]:at[i+1]+1], step)
		if !ok {
			continue
		}
		sm := smooth(heights, climbSmoothWindow)
		out[i].ClimbM, out[i].DescentM = sumClimb(sm, climbHysteresisM)
		out[i].OK = true
		// Sample k was read k*step along the leg; the last one is the leg's end.
		legLen := cum[at[i+1]] - cum[at[i]]
		out[i].HeightsM = heights
		out[i].DistM = make([]float64, len(heights))
		for k := range heights {
			if k == len(heights)-1 {
				out[i].DistM[k] = cum[at[i+1]]
			} else {
				out[i].DistM[k] = cum[at[i]] + math.Min(float64(k)*step, legLen)
			}
		}
	}
	return routeClimbResult{Legs: out, StopAtM: stopAt}
}

func matchStop(line [][2]float64, from int, lat, lon float64) int {
	best, bestD := from, math.MaxFloat64
	for j := from; j < len(line); j++ {
		d := distM(line[j][0], line[j][1], lat, lon)
		if d <= stopMatchM {
			return j
		}
		if d < bestD {
			best, bestD = j, d
		}
	}
	return best
}

// legHeights samples the DEM every step metres along the leg (and at its end). It fails, with no partial result, when any height is missing.
func legHeights(ctx context.Context, el ElevationBlocking, leg [][2]float64, step float64) ([]float64, bool) {
	var hs []float64
	read := func(p [2]float64) bool {
		h, ok := el.LookupBlocking(ctx, p[0], p[1])
		if !ok {
			return false
		}
		hs = append(hs, h)
		return true
	}
	if !read(leg[0]) {
		return nil, false
	}
	next := step
	run := 0.0
	for j := 1; j < len(leg); j++ {
		seg := distM(leg[j-1][0], leg[j-1][1], leg[j][0], leg[j][1])
		for seg > 0 && run+seg >= next {
			f := (next - run) / seg
			p := [2]float64{leg[j-1][0] + f*(leg[j][0]-leg[j-1][0]), leg[j-1][1] + f*(leg[j][1]-leg[j-1][1])}
			if !read(p) {
				return nil, false
			}
			next += step
		}
		run += seg
	}
	if !read(leg[len(leg)-1]) {
		return nil, false
	}
	return hs, true
}

// smooth is a centred moving average of width w (shorter at the ends).
func smooth(v []float64, w int) []float64 {
	if w <= 1 || len(v) < 2 {
		return v
	}
	out := make([]float64, len(v))
	half := w / 2
	for i := range v {
		lo, hi := i-half, i+half
		if lo < 0 {
			lo = 0
		}
		if hi > len(v)-1 {
			hi = len(v) - 1
		}
		sum := 0.0
		for _, x := range v[lo : hi+1] {
			sum += x
		}
		out[i] = sum / float64(hi-lo+1)
	}
	return out
}

// sumClimb adds up the rises and the falls along heights, counting a change only once it has moved hyst metres from the last counted height.
func sumClimb(h []float64, hyst float64) (climb, descent float64) {
	if len(h) == 0 {
		return 0, 0
	}
	ref := h[0]
	for _, x := range h[1:] {
		switch {
		case x-ref >= hyst:
			climb += x - ref
			ref = x
		case ref-x >= hyst:
			descent += ref - x
			ref = x
		}
	}
	return climb, descent
}

func lineLengthM(line [][2]float64) float64 {
	t := 0.0
	for j := 1; j < len(line); j++ {
		t += distM(line[j-1][0], line[j-1][1], line[j][0], line[j][1])
	}
	return t
}
