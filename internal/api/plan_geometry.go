package api

import (
	"context"
	"errors"
	"log/slog"
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

// withPlanGeometry puts the router on the request's context for the plan handlers (a nil router: no geometry, as before).
func withPlanGeometry(ctx context.Context, r geometryRouter, m *metrics.Metrics) context.Context {
	return context.WithValue(ctx, planGeometryKey{}, planGeometry{router: r, metrics: m})
}

// maxGeometryRoutes is how many of a plan's candidate routes get a line: the recommended first, and a couple of alternatives.
const maxGeometryRoutes = 3

// geometryBudget bounds all the routing of one plan (a multi-day plan has one per day, each bounded again by the handler's own timeout).
const geometryBudget = 8 * time.Second

// withGeometry is a copy of the plan whose routes carry Geometry (polyline6) and GeometryPrecision. The input is never changed (a plan can be
// shared). It is best effort: with no router, or on any failure, the route has no line and the plan is served as it was.
func withGeometry(ctx context.Context, in *rivian.TripPlan, opts maps.RouteOpts) *rivian.TripPlan {
	pg, ok := ctx.Value(planGeometryKey{}).(planGeometry)
	if !ok || pg.router == nil || in == nil || !pg.router.Enabled() {
		return in
	}
	plan := *in
	plan.Routes = append([]rivian.TripRoute(nil), in.Routes...)
	ctx, cancel := context.WithTimeout(ctx, geometryBudget)
	defer cancel()
	for i := range plan.Routes {
		if i >= maxGeometryRoutes {
			break
		}
		r := &plan.Routes[i]
		locs := make([][2]float64, 0, len(r.Waypoints))
		for _, w := range r.Waypoints {
			if validLatLon(w.Latitude, w.Longitude) {
				locs = append(locs, [2]float64{w.Latitude, w.Longitude})
			}
		}
		if len(locs) < 2 {
			continue
		}
		rt, err := pg.router.RouteChecked(ctx, locs, opts)
		if err != nil {
			var bad *maps.BadShapeError
			if errors.As(err, &bad) && pg.metrics != nil {
				pg.metrics.RoutePointRefusedTotal.WithLabelValues("plan_" + bad.Kind).Inc()
			}
			slog.WarnContext(ctx, "plan geometry: no line for a route", "class", geometryErrClass(err)) // no coordinates, no gateway text
			continue
		}
		var line [][2]float64
		for k, leg := range rt.Legs {
			shape := leg.Shape
			if k > 0 && len(shape) > 0 {
				shape = shape[1:] // the previous leg already ends at this vertex
			}
			line = append(line, shape...)
		}
		if len(line) < 2 {
			continue
		}
		r.Geometry, r.GeometryPrecision = maps.EncodePolyline6(line), 6
	}
	return &plan
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
