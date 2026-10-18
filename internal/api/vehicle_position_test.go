package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// fixedPosition makes the truck "be" somewhere for the test.
func fixedPosition(t *testing.T, pos vehiclePos, ok bool) *int {
	t.Helper()
	calls := new(int)
	old := vehiclePositionFor
	vehiclePositionFor = func(context.Context, *rivian.StateMonitor, *sql.DB, uuid.UUID, string, time.Time) (vehiclePos, bool) {
		*calls++
		return pos, ok
	}
	t.Cleanup(func() { vehiclePositionFor = old })
	return calls
}

var truckAt = vehiclePos{VehicleID: "v1", Lat: 35.37, Lon: -119.02, SoC: 61.5, At: time.Date(2026, 10, 21, 17, 40, 0, 0, time.UTC)}

func TestMultidayReplanFromTheTrucksPosition(t *testing.T) {
	fixedPosition(t, truckAt, true)
	p := &daysPlanner{}
	// The remaining days only; the origin and the SoC the SPA sends are not used.
	code, out := runMultiday(t, p, `{`+mdVehicle+`"origin_from_vehicle":true,"start_at":"2026-10-21T17:45:00Z",
	  "origin":{"lat":1,"lon":1},
	  "days":[{"end":{"lat":37.79,"lon":-122.39,"label":"SF"},"overnight":{"l2_kw":0}},{"end":{"lat":34.16,"lon":-118.17}}]}`)
	if code != 200 || len(p.calls) != 2 {
		t.Fatalf("status %d (%v), planner calls %d", code, out, len(p.calls))
	}
	first := p.calls[0]
	if first.Waypoints[0].Latitude != 35.37 || first.Waypoints[0].Longitude != -119.02 || first.StartingSoC != 61.5 {
		t.Errorf("day 1 started at %v with %v %%, want the truck's 35.37,-119.02 and 61.5", first.Waypoints[0], first.StartingSoC)
	}
	fp, _ := out["from_position"].(map[string]any)
	if fp["lat"] != 35.37 || fp["lon"] != -119.02 || fp["soc"] != 61.5 || fp["at"] != "2026-10-21T17:40:00Z" {
		t.Errorf("from_position = %v", out["from_position"])
	}
	d1 := out["days"].([]any)[0].(map[string]any)
	if d1["departure_soc"] != 61.5 || d1["start"].(map[string]any)["label"] != "Current location" {
		t.Errorf("day 1 = %v", d1["start"])
	}
	// Without the flag nothing about the truck is used, and nothing is echoed.
	p2 := &daysPlanner{}
	_, out2 := runMultiday(t, p2, `{`+mdVehicle+`"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`)
	if p2.calls[0].Waypoints[0].Latitude != 1 || p2.calls[0].StartingSoC != 90 || out2["from_position"] != nil {
		t.Errorf("without the flag: %v soc %v echo %v", p2.calls[0].Waypoints[0], p2.calls[0].StartingSoC, out2["from_position"])
	}
}

func TestReplanWithoutAPositionIs409(t *testing.T) {
	calls := fixedPosition(t, vehiclePos{}, false)
	p := &daysPlanner{}
	code, out := runMultiday(t, p, `{`+mdVehicle+`"origin_from_vehicle":true,"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`)
	msg, _ := out["error"].(string)
	if code != http.StatusConflict || out["class"] != "no_position" || !strings.Contains(msg, "position isn't available") || len(p.calls) != 0 || *calls != 1 {
		t.Errorf("multi-day: %d %v planner calls %d", code, out, len(p.calls))
	}
	// Single-day too.
	cp := &capturePlanner{}
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"origin_from_vehicle":true,`))))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body["class"] != "no_position" {
		t.Errorf("single-day: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSingleDayReplanFromThePosition(t *testing.T) {
	fixedPosition(t, truckAt, true)
	cp := &capturePlanner{}
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"origin_from_vehicle":true,"starting_range_meters":99999,`))))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	in := cp.got
	if in.Waypoints[0].Latitude != 35.37 || in.Waypoints[0].Longitude != -119.02 || in.Waypoints[1].Latitude != 32.71 {
		t.Errorf("waypoints = %+v: the origin is the truck, the destination is kept", in.Waypoints)
	}
	if in.StartingSoC != 61.5 || in.StartingRangeMeters != 0 {
		t.Errorf("start %v %% range %v, want the truck's charge and a range derived from it", in.StartingSoC, in.StartingRangeMeters)
	}
	var body struct {
		FromPosition *fromPosition `json:"from_position"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.FromPosition == nil || body.FromPosition.SoC != 61.5 {
		t.Errorf("from_position = %+v", body.FromPosition)
	}
}

func TestCurrentPositionWithNoSources(t *testing.T) {
	if _, ok := currentPosition(context.Background(), nil, nil, uuid.New(), "v1", time.Now()); ok {
		t.Error("no monitor and no database is no position")
	}
	if _, ok := currentPosition(context.Background(), nil, nil, uuid.New(), "", time.Now()); ok {
		t.Error("no vehicle is no position")
	}
}
