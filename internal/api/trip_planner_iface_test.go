package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// plannerClient is a client with a trip planner, as the replay client will be.
type plannerClient struct {
	rivian.Client
	rivian.TripPlanner
}

func fixtureClient() plannerClient {
	f := rivian.PlanFixture{
		Origin: [2]float64{34.0562, -118.2427}, Destination: [2]float64{32.7157, -117.1611}, StartSoC: 80,
		Plan: rivian.TripPlan{Status: "OK", Routes: []rivian.TripRoute{{DestinationReached: true, ArrivalSoC: 55,
			Waypoints: []rivian.PlannedWaypoint{
				{WaypointType: "ORIGIN", DepartureSoC: 80, DepartureTimeUTC: "2026-11-14T16:00:00Z"},
				{WaypointType: "DESTINATION", ArrivalSoC: 55, ArrivalTimeUTC: "2026-11-14T18:00:00Z"},
			}}}},
	}
	return plannerClient{
		Client:      rivian.NewStub(),
		TripPlanner: rivian.NewFixturePlanner([]rivian.PlanFixture{f}, func() time.Time { return time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC) }),
	}
}

func TestTripPlannerOf(t *testing.T) {
	if _, ok := tripPlannerOf(rivian.NewStub()); ok {
		t.Error("the stub client has no trip planner")
	}
	if _, ok := tripPlannerOf(rivian.NewMock()); ok {
		t.Error("the mock client has no trip planner")
	}
	var nilLive *rivian.LiveClient
	if _, ok := tripPlannerOf(nilLive); ok {
		t.Error("a nil live client is not a planner")
	}
	if _, ok := tripPlannerOf(fixtureClient()); !ok {
		t.Error("a client with a planner should be one")
	}
}

const planBody = `{"vehicle_id":"v1","starting_soc":80,"waypoints":[
 {"latitude":34.0562,"longitude":-118.2427,"waypoint_type":"origin"},
 {"latitude":32.7157,"longitude":-117.1611,"waypoint_type":"destination"}]}`

func TestHandleTripPlanServesAFixturePlanAsSimulated(t *testing.T) {
	h := handleTripPlan(fixtureClient(), nil, nil, uuid.New(), nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Simulated bool `json:"simulated"`
		Routes    []struct {
			Waypoints []struct{ DepartureTimeUTC string }
		} `json:"Routes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Simulated || len(out.Routes) != 1 || out.Routes[0].Waypoints[0].DepartureTimeUTC != "2026-12-01T08:00:00Z" {
		t.Errorf("response = %s", rec.Body.String())
	}

	// A trip with no fixture is a clear 404-class answer, not a 500.
	miss := bytes.NewBufferString(`{"vehicle_id":"v1","starting_soc":80,"waypoints":[
	 {"latitude":40,"longitude":-100,"waypoint_type":"origin"},{"latitude":41,"longitude":-101,"waypoint_type":"destination"}]}`)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan", miss))
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte("no sample plan")) {
		t.Errorf("no fixture = %d %s, want 404 with an explanation", rec.Code, rec.Body.String())
	}
}

func TestTripPlanHandlersStillRefuseClientsWithoutAPlanner(t *testing.T) {
	for name, c := range map[string]rivian.Client{"stub": rivian.NewStub(), "mock": rivian.NewMock()} {
		rec := httptest.NewRecorder()
		handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBody)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s single-day = %d, want 404", name, rec.Code)
		}
		rec = httptest.NewRecorder()
		handleTripPlanMultiday(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan/multiday", bytes.NewBufferString(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s multi-day = %d, want 404", name, rec.Code)
		}
	}
	_ = context.Background
}

// Planner errors that are the caller's, or the install's, are not upstream failures.
func TestHandleTripPlanMapsPlannerErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"invalid trip": {rivian.ErrInvalidTrip, http.StatusBadRequest},
		"no planner":   {rivian.ErrNoPlanner, http.StatusNotFound},
		"no fixture":   {rivian.ErrNoFixture, http.StatusNotFound},
	} {
		c := plannerClient{Client: rivian.NewStub(), TripPlanner: errPlanner{tc.err}}
		rec := httptest.NewRecorder()
		handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBody)))
		if rec.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

type errPlanner struct{ err error }

func (e errPlanner) PlanTrip(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
	return nil, e.err
}
