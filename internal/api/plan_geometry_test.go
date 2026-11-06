package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	// two legs of three vertices each, the shared stop once: 5 vertices, from the origin to the destination
	if len(pts) != 5 || pts[0] != [2]float64{34, -118} || pts[len(pts)-1] != [2]float64{36, -116} {
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
