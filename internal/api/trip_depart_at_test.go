package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// capturePlanner records the input it was asked to plan.
type capturePlanner struct{ got rivian.PlanTripInput }

func (c *capturePlanner) PlanTrip(_ context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	c.got = in
	return &rivian.TripPlan{Status: "OK"}, nil
}

func planWith(extra string) string {
	return `{"vehicle_id":"v1","starting_soc":80,` + extra + `"waypoints":[
	 {"latitude":34.05,"longitude":-118.24,"waypoint_type":"origin"},
	 {"latitude":32.71,"longitude":-117.16,"waypoint_type":"destination"}]}`
}

func TestTripPlanDepartAt(t *testing.T) {
	var lastType string
	run := func(body string) (int, *capturePlanner) {
		cp := &capturePlanner{}
		rec := httptest.NewRecorder()
		defer func() { lastType = rec.Header().Get("Content-Type") }()
		handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
			httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(body)))
		return rec.Code, cp
	}
	code, cp := run(planWith(`"depart_at":"2026-12-24T08:30:00-08:00",`))
	if code != http.StatusOK || !cp.got.DepartureTime.Equal(time.Date(2026, 12, 24, 16, 30, 0, 0, time.UTC)) {
		t.Errorf("depart_at: %d, departure %v", code, cp.got.DepartureTime)
	}
	if code, cp := run(planWith("")); code != http.StatusOK || !cp.got.DepartureTime.IsZero() {
		t.Errorf("no depart_at: %d, departure %v (want zero: now)", code, cp.got.DepartureTime)
	}
	for _, bad := range []string{"tomorrow", "2026-12-24", "2026-12-24 08:30:00", "1782300000"} {
		if code, _ := run(planWith(`"depart_at":"` + bad + `",`)); code != http.StatusBadRequest || !strings.HasPrefix(lastType, "application/json") {
			t.Errorf("depart_at %q = %d (%s), want a JSON 400", bad, code, lastType)
		}
	}
}

// A far-future depart_at is turned into the planner's invalid-trip error, which
// the handler answers 400.
func TestTripPlanFarFutureDepartAtIs400(t *testing.T) {
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: errPlanner{rivian.ErrInvalidTrip}}
	rec := httptest.NewRecorder()
	handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"depart_at":"9999-12-31T00:00:00Z",`))))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoutePreferencesFromTheRequest(t *testing.T) {
	var nilPrefs *tripRoutePrefs
	if nilPrefs.toRivian() != nil || (&tripRoutePrefs{}).toRivian() != nil {
		t.Error("no preference, or an empty one, must be nil so the request is unchanged")
	}
	if got := (&tripRoutePrefs{AvoidTolls: true, AvoidHighways: true}).toRivian(); got == nil || !got.AvoidTolls || got.AvoidFerries || !got.AvoidHighways {
		t.Errorf("toRivian = %+v", got)
	}
	// Single-day.
	cp := &capturePlanner{}
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"route_preferences":{"avoid_tolls":true,"avoid_ferries":false,"avoid_highways":true},`))))
	if rec.Code != 200 || cp.got.RoutePreferences == nil || !cp.got.RoutePreferences.AvoidTolls || !cp.got.RoutePreferences.AvoidHighways || cp.got.RoutePreferences.AvoidFerries {
		t.Errorf("single-day: %d %+v", rec.Code, cp.got.RoutePreferences)
	}
	cp = &capturePlanner{}
	rec = httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"route_preferences":{},`))))
	if cp.got.RoutePreferences != nil {
		t.Error("an empty route_preferences must not reach the planner")
	}
}
