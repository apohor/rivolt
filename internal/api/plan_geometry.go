package api

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
)

// A served plan carries the road line through its stops, so a map can draw each route and each day without routing it again. It is built here
// from OUR Valhalla (never Rivian: the gateway's own geometry field is empty and no call is added), asked for and read as polyline6, and every
// leg goes through the shape guard before it is kept: a leg off the globe, or nowhere near its two stops, drops the line (the plan is still
// served) and is counted in rivolt_route_point_refused_total{where="plan_offglobe"|"plan_detour"}. The line is built on the response copy and
// is not part of what a saved trip stores unless the client sends it back.

// geometryRouter is the part of maps.Valhalla the plan geometry uses.
type geometryRouter interface {
	Enabled() bool
	RouteChecked(ctx context.Context, locs [][2]float64, opts maps.RouteOpts) (maps.Route, error)
}

type planGeometryKey struct{}

type planGeometry struct {
	router  geometryRouter
	metrics *metrics.Metrics
}

type planElevationKey struct{}

// withPlanElevation puts the DEM on the request's context for the plan handlers (nil: no climb figures, as before).
func withPlanElevation(ctx context.Context, el ElevationBlocking) context.Context {
	if el == nil {
		return ctx
	}
	return context.WithValue(ctx, planElevationKey{}, el)
}

// withPlanGeometry puts the router on the request's context for the plan handlers (a nil router: no geometry, as before).
func withPlanGeometry(ctx context.Context, r geometryRouter, m *metrics.Metrics) context.Context {
	return context.WithValue(ctx, planGeometryKey{}, planGeometry{router: r, metrics: m})
}

// maxGeometryRoutes is how many of a plan's candidate routes get a line: the recommended first, and a couple of alternatives.
const maxGeometryRoutes = 3

// geometryToleranceM is how far the served line may sit from the road's own shape (meters): Douglas-Peucker thins the shape to it. 10 m
// is invisible on a phone map at the closest zoom a route is read at; ux can tune it.
const geometryToleranceM = 10.0

// maxGeometryPoints caps the points of one route's line (one route of one day): about 24 KB as polyline6. The tolerance doubles until a
// long day fits, and past eight doublings the line is thinned evenly, the stops kept.
const maxGeometryPoints = 4000

// geometryBudget bounds all the routing of one response: every day of a multi-day plan shares it.
var geometryBudget = 8 * time.Second // a var only so tests can move it

// withGeometry is a copy of the plan whose routes carry Geometry (polyline6) and GeometryPrecision. The input is never changed (a plan can be
// shared). It is best effort: with no router, or on any failure, the route has no line and the plan is served as it was.
// observePlanStage records how long one of our own stages around a planner call took, when the request carries the metrics.
func observePlanStage(ctx context.Context, stage string, start time.Time) {
	if pg, ok := ctx.Value(planGeometryKey{}).(planGeometry); ok && pg.metrics != nil {
		pg.metrics.TripPlanStageSeconds.WithLabelValues(stage).Observe(time.Since(start).Seconds())
	}
}

func withGeometry(ctx context.Context, in *rivian.TripPlan, opts maps.RouteOpts) *rivian.TripPlan {
	return withGeometryAll(ctx, []*rivian.TripPlan{in}, opts)[0]
}

// geometryConcurrency caps the Valhalla routes one request has in flight. A plan is at most maxGeometryRoutes routes a day and a multi-day
// plan has several days, but one request never asks for more than this at once, so a busy fleet of pods cannot multiply it.
const geometryConcurrency = 3

// withGeometryAll is withGeometry for several plans (the days of a multi-day plan) under ONE geometryBudget for the whole response, with at
// most geometryConcurrency routes in flight. A route that is not drawn before the budget ends has no line, as with any other failure.
func withGeometryAll(ctx context.Context, ins []*rivian.TripPlan, opts maps.RouteOpts) []*rivian.TripPlan {
	pg, ok := ctx.Value(planGeometryKey{}).(planGeometry)
	if !ok || pg.router == nil || !pg.router.Enabled() {
		return ins
	}
	defer observePlanStage(ctx, "geometry", time.Now())
	ctx, cancel := context.WithTimeout(ctx, geometryBudget)
	defer cancel()
	outs := make([]*rivian.TripPlan, len(ins))
	type job struct{ plan, route int }
	for pi, in := range ins {
		if in == nil {
			continue
		}
		plan := *in
		plan.Routes = append([]rivian.TripRoute(nil), in.Routes...)
		outs[pi] = &plan
	}
	// Every day's recommended route first, then the alternatives: when the shared budget runs short, it is the alternatives of the late
	// days that go undrawn, never the primary line of any day.
	var jobs []job
	for ri := 0; ri < maxGeometryRoutes; ri++ {
		for pi, plan := range outs {
			if plan != nil && ri < len(plan.Routes) {
				jobs = append(jobs, job{pi, ri})
			}
		}
	}
	sem := make(chan struct{}, geometryConcurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(r *rivian.TripRoute) {
			defer wg.Done()
			defer func() { <-sem }()
			routeGeometry(ctx, pg, r, opts)
		}(&outs[j.plan].Routes[j.route])
	}
	wg.Wait()
	return outs
}

// routeGeometry fills Geometry and GeometryPrecision of one route, or leaves it as it was.
func routeGeometry(ctx context.Context, pg planGeometry, r *rivian.TripRoute, opts maps.RouteOpts) {
	locs := make([][2]float64, 0, len(r.Waypoints))
	for _, w := range r.Waypoints {
		if validLatLon(w.Latitude, w.Longitude) {
			locs = append(locs, [2]float64{w.Latitude, w.Longitude})
		}
	}
	if len(locs) < 2 {
		return
	}
	count := func(outcome string) {
		if pg.metrics != nil {
			pg.metrics.PlanGeometryRoutesTotal.WithLabelValues(outcome).Inc()
		}
	}
	if ctx.Err() != nil { // the response's shared budget ended before this route was asked for
		count("budget")
		slog.WarnContext(ctx, "plan geometry: no line for a route", "class", "budget")
		return
	}
	rt, err := pg.router.RouteChecked(ctx, locs, opts)
	if err != nil {
		if ctx.Err() != nil { // asked, and the shared budget ended on it
			count("budget")
			slog.WarnContext(ctx, "plan geometry: no line for a route", "class", "budget")
			return
		}
		count("failed")
		var bad *maps.BadShapeError
		if errors.As(err, &bad) && pg.metrics != nil {
			pg.metrics.RoutePointRefusedTotal.WithLabelValues("plan_" + bad.Kind).Inc()
		}
		slog.WarnContext(ctx, "plan geometry: no line for a route", "class", geometryErrClass(err)) // no coordinates, no gateway text
		return
	}
	// The road's own shape has a vertex every few meters: thin it to a tolerance the map cannot show, keep the stops, cap the points,
	// and check the thinned legs again with the same guard as the raw ones.
	shapes := make([][][2]float64, len(rt.Legs))
	for k, leg := range rt.Legs {
		shapes[k] = leg.Shape
	}
	thin := maps.SimplifyLegs(shapes, geometryToleranceM, maxGeometryPoints)
	bad := false
	for k, shape := range thin {
		if k+1 < len(locs) && maps.CheckShape(shape, locs[k], locs[k+1]) != nil {
			bad = true
		}
	}
	if bad {
		count("failed")
		slog.WarnContext(ctx, "plan geometry: no line for a route", "class", "bad_shape_simplified")
		return
	}
	line := maps.JoinLegs(thin)
	if len(line) < 2 {
		count("failed")
		return
	}
	count("drawn")
	r.Geometry, r.GeometryPrecision = maps.EncodePolyline6(line), 6
}

func geometryErrClass(err error) string {
	var bad *maps.BadShapeError
	switch {
	case errors.As(err, &bad):
		return "bad_shape_" + bad.Kind
	case errors.Is(err, maps.ErrNoRoute):
		return "no_route"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "unavailable"
}

func routeOptsOf(p *rivian.RoutePreferences) maps.RouteOpts {
	if p == nil {
		return maps.RouteOpts{}
	}
	return maps.RouteOpts{AvoidTolls: p.AvoidTolls, AvoidFerries: p.AvoidFerries, AvoidHighways: p.AvoidHighways}
}

// geometryRouter is Valhalla as the interface the plan geometry takes; nil (not a typed nil) when none is configured.
func (d Deps) geometryRouter() geometryRouter {
	if d.Valhalla == nil {
		return nil
	}
	return d.Valhalla
}
