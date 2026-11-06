package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
)

type fakeGeoRouter struct {
	enabled bool
	err     error
	calls   int
	locs    [][][2]float64
}

func (f *fakeGeoRouter) Enabled() bool { return f.enabled }
func (f *fakeGeoRouter) RouteChecked(_ context.Context, locs [][2]float64, _ maps.RouteOpts) (maps.Route, error) {
	f.calls++
	f.locs = append(f.locs, locs)
	if f.err != nil {
		return maps.Route{}, f.err
	}
	var rt maps.Route
	for i := 0; i+1 < len(locs); i++ {
		rt.Legs = append(rt.Legs, maps.RouteLeg{Shape: [][2]float64{locs[i], {(locs[i][0] + locs[i+1][0]) / 2, (locs[i][1] + locs[i+1][1]) / 2}, locs[i+1]}})
	}
	return rt, nil
}

func geoPlan(routes int) *rivian.TripPlan {
	p := &rivian.TripPlan{}
	for i := 0; i < routes; i++ {
		p.Routes = append(p.Routes, rivian.TripRoute{Waypoints: []rivian.PlannedWaypoint{
			{WaypointType: "ORIGIN", Latitude: 34, Longitude: -118},
			{WaypointType: "DC_CHARGE_STATION", Latitude: 35, Longitude: -117},
			{WaypointType: "DESTINATION", Latitude: 36, Longitude: -116},
		}})
	}
	return p
}

func TestPlanGeometryIsPolyline6ThroughTheStopsOnACopy(t *testing.T) {
	r := &fakeGeoRouter{enabled: true}
	in := geoPlan(1)
	ctx := withPlanGeometry(context.Background(), r, nil)
	out := withGeometry(ctx, in, maps.RouteOpts{})
	rt := out.Routes[0]
	if rt.GeometryPrecision != 6 || rt.Geometry == "" {
		t.Fatalf("no line: %+v", rt)
	}
	pts := maps.DecodePolyline6(rt.Geometry)
	// two straight legs: the midpoints are on the line and thin away; the origin, the stop and the destination stay
	if len(pts) != 3 || pts[0] != [2]float64{34, -118} || pts[1] != [2]float64{35, -117} || pts[len(pts)-1] != [2]float64{36, -116} {
		t.Errorf("line = %v", pts)
	}
	if len(r.locs) != 1 || len(r.locs[0]) != 3 {
		t.Errorf("the router was asked %v, want one request through the 3 stops", r.locs)
	}
	if in.Routes[0].Geometry != "" || out == in {
		t.Error("the input plan was changed or returned as is")
	}
}

func TestPlanGeometryOnlyForTheFirstRoutesAndOnlyWithARouter(t *testing.T) {
	r := &fakeGeoRouter{enabled: true}
	out := withGeometry(withPlanGeometry(context.Background(), r, nil), geoPlan(5), maps.RouteOpts{})
	if r.calls != maxGeometryRoutes {
		t.Errorf("%d routing calls for 5 routes, want %d", r.calls, maxGeometryRoutes)
	}
	if out.Routes[maxGeometryRoutes].Geometry != "" || out.Routes[0].Geometry == "" {
		t.Error("only the first routes get a line")
	}
	in := geoPlan(1)
	for name, ctx := range map[string]context.Context{
		"no router on the context": context.Background(),
		"a nil router":             withPlanGeometry(context.Background(), nil, nil),
		"a router that is off":     withPlanGeometry(context.Background(), &fakeGeoRouter{enabled: false}, nil),
	} {
		if got := withGeometry(ctx, in, maps.RouteOpts{}); got != in {
			t.Errorf("%s: the plan must be returned as it was", name)
		}
	}
	if withGeometry(withPlanGeometry(context.Background(), r, nil), nil, maps.RouteOpts{}) != nil {
		t.Error("nil plan must stay nil")
	}
}

// A line that fails the shape guard is left out, counted, and the plan is still served; any other failure just leaves no line.
func TestPlanGeometryDropsARefusedLineAndCountsIt(t *testing.T) {
	m := metrics.New()
	r := &fakeGeoRouter{enabled: true, err: &maps.BadShapeError{Kind: "detour", Reason: "strays"}}
	out := withGeometry(withPlanGeometry(context.Background(), r, m), geoPlan(1), maps.RouteOpts{})
	if out.Routes[0].Geometry != "" || len(out.Routes[0].Waypoints) != 3 {
		t.Errorf("a refused line left a geometry or damaged the plan: %+v", out.Routes[0])
	}
	if got := testutil.ToFloat64(m.RoutePointRefusedTotal.WithLabelValues("plan_detour")); got != 1 {
		t.Errorf("plan_detour = %v, want 1", got)
	}
	r = &fakeGeoRouter{enabled: true, err: errors.New("valhalla: HTTP 503")}
	out = withGeometry(withPlanGeometry(context.Background(), r, m), geoPlan(1), maps.RouteOpts{})
	if out.Routes[0].Geometry != "" {
		t.Error("a routing failure left a geometry")
	}
	if got := testutil.ToFloat64(m.RoutePointRefusedTotal.WithLabelValues("plan_detour")); got != 1 {
		t.Errorf("a routing failure must not count as a refusal: %v", got)
	}
}

// Both plan handlers put the line on the served routes (single-day, and each day of a multi-day plan) when the router is on the request's context.
func TestPlanHandlersServeTheLine(t *testing.T) {
	r := &fakeGeoRouter{enabled: true}
	body := `{"vehicle_id":"v1","starting_soc":80,"waypoints":[
	  {"latitude":34.05,"longitude":-118.24,"waypoint_type":"origin"},
	  {"latitude":32.71,"longitude":-117.16,"waypoint_type":"destination"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(body))
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: &daysPlanner{}}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		req.WithContext(withPlanGeometry(req.Context(), r, nil)))
	var out struct {
		Routes []rivian.TripRoute
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Routes) == 0 || out.Routes[0].Geometry == "" || out.Routes[0].GeometryPrecision != 6 {
		t.Fatalf("single-day: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMultidayHandlerServesALinePerDay(t *testing.T) {
	r := &fakeGeoRouter{enabled: true}
	body := `{` + mdVehicle + `"start_at":"2026-10-20T08:00:00Z",
	  "origin":{"latitude":34.05,"longitude":-118.24},
	  "days":[{"end":{"latitude":33.4484,"longitude":-112.0740},"overnight":{"l2_kw":7,"until":"07:00","max_soc_pct":80}},
	          {"end":{"latitude":39.7392,"longitude":-104.9903}}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/trips/plan-multiday", bytes.NewBufferString(body))
	handleTripPlanMultiday(plannerClient{Client: rivian.NewStub(), TripPlanner: &daysPlanner{}}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		req.WithContext(withPlanGeometry(req.Context(), r, nil)))
	var out struct {
		Days []struct {
			Plan struct{ Routes []rivian.TripRoute }
		}
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Days) != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for i, d := range out.Days {
		if len(d.Plan.Routes) == 0 || d.Plan.Routes[0].Geometry == "" {
			t.Errorf("day %d has no line: %+v", i+1, d.Plan.Routes)
			continue
		}
		// the stop labels (#1563) and the line compose: neither replaces the other
		if ws := d.Plan.Routes[0].Waypoints; len(ws) == 0 || ws[0].Source != "origin" {
			t.Errorf("day %d lost its stop labels next to the line: %+v", i+1, ws)
		}
	}
	if r.calls < 2 {
		t.Errorf("%d routing calls for 2 days", r.calls)
	}
}

// A long synthetic day: the line is thinned well under the cap, the stops stay in it, and the bytes drop.
func TestPlanGeometryIsThinnedCappedAndKeepsTheStops(t *testing.T) {
	r := &longGeoRouter{fakeGeoRouter: fakeGeoRouter{enabled: true}, pts: 60_000}
	ctx := withPlanGeometry(context.Background(), r, nil)
	out := withGeometry(ctx, geoPlan(1), maps.RouteOpts{})
	rt := out.Routes[0]
	pts := maps.DecodePolyline6(rt.Geometry)
	if len(pts) < 3 || len(pts) > maxGeometryPoints {
		t.Fatalf("%d points, want between 3 and %d", len(pts), maxGeometryPoints)
	}
	raw := len(maps.EncodePolyline6(r.last))
	t.Logf("raw %d points / %d bytes -> %d points / %d bytes", len(r.last), raw, len(pts), len(rt.Geometry))
	if len(rt.Geometry) > raw/5 {
		t.Errorf("only %d of %d bytes saved", raw-len(rt.Geometry), raw)
	}
	has := func(p [2]float64) bool {
		for _, q := range pts {
			if math.Abs(q[0]-p[0]) < 2e-6 && math.Abs(q[1]-p[1]) < 2e-6 {
				return true
			}
		}
		return false
	}
	for _, stop := range [][2]float64{{34, -118}, {35, -117}, {36, -116}} {
		if !has(stop) {
			t.Errorf("stop %v is not on the served line", stop)
		}
	}
}

// The guard still runs on the thinned line: a leg that wanders far from its ends, which thinning keeps (it sticks out), is refused.
func TestPlanGeometryGuardStillRefusesAStrayingLineAfterThinning(t *testing.T) {
	r := &longGeoRouter{fakeGeoRouter: fakeGeoRouter{enabled: true}, pts: 2000, stray: true}
	ctx := withPlanGeometry(context.Background(), r, nil)
	out := withGeometry(ctx, geoPlan(1), maps.RouteOpts{})
	if out.Routes[0].Geometry != "" {
		t.Error("a line that strays thousands of km from its ends was served")
	}
}

// longGeoRouter answers each leg with a many-vertex synthetic road between its two ends: a gentle wobble, optionally one far-off vertex.
type longGeoRouter struct {
	fakeGeoRouter
	pts   int
	stray bool
	mu    sync.Mutex // the what-if route both sides of an edit at once
	last  [][2]float64
}

func (f *longGeoRouter) RouteChecked(ctx context.Context, locs [][2]float64, o maps.RouteOpts) (maps.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rt maps.Route
	f.last = nil
	for i := 0; i+1 < len(locs); i++ {
		a, b := locs[i], locs[i+1]
		shape := make([][2]float64, f.pts)
		for k := range shape {
			t := float64(k) / float64(f.pts-1)
			shape[k] = [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t + 0.002*math.Sin(t*200)}
		}
		shape[0], shape[len(shape)-1] = a, b
		if f.stray {
			shape[len(shape)/2] = [2]float64{a[0] + 30, a[1] + 40}
		}
		rt.Legs = append(rt.Legs, maps.RouteLeg{Shape: shape})
		if i > 0 {
			shape = shape[1:]
		}
		f.last = append(f.last, shape...)
	}
	return rt, nil
}
