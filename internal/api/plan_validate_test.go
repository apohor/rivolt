package api

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// The plan endpoints' tests run at a fixed "now", so the departure window does
// not drift as the calendar does.
var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func init() { planNow = func() time.Time { return testNow } }

func TestDepartureWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		t  time.Time
		ok bool
	}{
		"now":           {testNow, true},
		"13 days ago":   {testNow.Add(-13 * 24 * time.Hour), true},
		"15 days ago":   {testNow.Add(-15 * 24 * time.Hour), false},
		"29 days ahead": {testNow.Add(29 * 24 * time.Hour), true},
		"31 days ahead": {testNow.Add(31 * 24 * time.Hour), true},
		"179 days":      {testNow.Add(179 * 24 * time.Hour), true},
		"181 days":      {testNow.Add(181 * 24 * time.Hour), false},
		"year 9999":     {time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"the beginning": {time.Time{}, false},
	} {
		if err := departureWindowErr(tc.t, testNow); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, c := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}} {
		if validLatLon(c[0], c[1]) {
			t.Errorf("%v accepted", c)
		}
	}
	if !validLatLon(90, 180) || !validLatLon(-90, -180) || !validLatLon(0, 0) {
		t.Error("the edges are valid")
	}
}

func TestMultidayRejectsOutOfRangeInputs(t *testing.T) {
	ok := `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]`
	for name, tc := range map[string]struct{ body, want string }{
		"soc over 100":         {`{"vehicle_id":"v1","starting_soc":120,"pack_kwh":131,` + ok + `}`, "starting_soc"},
		"soc zero":             {`{"vehicle_id":"v1","starting_soc":0,"pack_kwh":131,` + ok + `}`, "starting_soc"},
		"pack huge":            {`{"vehicle_id":"v1","starting_soc":90,"pack_kwh":99999,` + ok + `}`, "pack_kwh"},
		"target over 100":      {`{` + mdVehicle + `"target_arrival_soc_percent":150,` + ok + `}`, "target_arrival_soc_percent"},
		"cap over 100":         {`{` + mdVehicle + `"max_overnight_soc_pct":101,` + ok + `}`, "max_overnight_soc_pct"},
		"start_at far ahead":   {`{` + mdVehicle + `"start_at":"2027-06-01T08:00:00Z",` + ok + `}`, "start_at"},
		"start_at long ago":    {`{` + mdVehicle + `"start_at":"2026-01-01T08:00:00Z",` + ok + `}`, "start_at"},
		"origin latitude":      {`{` + mdVehicle + `"origin":{"lat":95,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`, "origin"},
		"end longitude":        {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":200}}]}`, "day 1 end"},
		"via latitude":         {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}},{"vias":[{"lat":1,"lon":1},{"lat":-91,"lon":0}],"end":{"lat":3,"lon":3}}]}`, "day 2 via 2"},
		"day target":           {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"target_arrival_soc_pct":101}]}`, "day 1"},
		"day depart_at":        {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"depart_at":"2030-01-01T00:00:00Z"}]}`, "day 1: depart_at"},
		"overnight l2":         {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"overnight":{"l2_kw":900}},{"end":{"lat":3,"lon":3}}]}`, "l2_kw"},
		"overnight cap":        {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"overnight":{"max_soc_pct":150}},{"end":{"lat":3,"lon":3}}]}`, "max_soc_pct"},
		"legacy overnight":     {`{` + mdVehicle + `"origin":{"latitude":1,"longitude":1},"destination":{"latitude":3,"longitude":3},"overnights":[{"latitude":99,"longitude":0}]}`, "overnight 1"},
		"from vehicle, origin": {`{` + mdVehicle + `"origin_from_vehicle":true,"days":[{"end":{"lat":2,"lon":300}}]}`, "day 1 end"},
	} {
		p := &daysPlanner{}
		code, out := runMultiday(t, p, tc.body)
		msg, _ := out["error"].(string)
		if code != 400 || !strings.Contains(msg, tc.want) || len(p.calls) != 0 {
			t.Errorf("%s: %d %q (planner calls %d), want a 400 mentioning %q", name, code, msg, len(p.calls), tc.want)
		}
	}
	// A request inside the window is accepted, and with origin_from_vehicle the
	// origin may be omitted.
	fixedPosition(t, truckAt, true)
	if code, out := runMultiday(t, &daysPlanner{}, `{`+mdVehicle+`"origin_from_vehicle":true,"start_at":"2026-10-20T13:00:00Z","days":[{"end":{"lat":2,"lon":2}}]}`); code != 200 {
		t.Errorf("a valid re-plan: %d %v", code, out)
	}
}

func TestSingleDayRejectsOutOfRangeInputs(t *testing.T) {
	for name, extra := range map[string]string{
		"soc":                `"starting_soc":150,`,
		"target":             `"target_arrival_soc_percent":-5,`,
		"bearing negative":   `"origin_bearing":-1,`,
		"bearing too big":    `"origin_bearing":360,`,
		"depart_at far":      `"depart_at":"2027-06-01T08:00:00Z",`,
		"depart_at long ago": `"depart_at":"2026-01-01T08:00:00Z",`,
	} {
		cp := &capturePlanner{}
		rec := httptest.NewRecorder()
		handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
			httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBodyWithout(extra))))
		if rec.Code != 400 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	for name, wps := range map[string]string{
		"latitude":  `[{"latitude":95,"longitude":0,"waypoint_type":"origin"},{"latitude":1,"longitude":1,"waypoint_type":"destination"}]`,
		"longitude": `[{"latitude":1,"longitude":1,"waypoint_type":"origin"},{"latitude":1,"longitude":-200,"waypoint_type":"destination"}]`,
	} {
		rec := httptest.NewRecorder()
		handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: &capturePlanner{}}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
			httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(`{"vehicle_id":"v1","starting_soc":80,"waypoints":`+wps+`}`)))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid coordinate") {
			t.Errorf("waypoint %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// A normal request, with a bearing and a target, still plans.
	cp := &capturePlanner{}
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: cp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBodyWithout(`"origin_bearing":140,"target_arrival_soc_percent":10,"depart_at":"2026-10-21T08:00:00Z",`))))
	if rec.Code != 200 {
		t.Errorf("a valid request: %d %s", rec.Code, rec.Body.String())
	}
}

// planBodyWithout is planWith (a vehicle, an SoC and two waypoints) without the
// SoC, so a test can set its own.
func planBodyWithout(extra string) string {
	body := planWith(extra)
	if strings.Contains(extra, "starting_soc") {
		body = strings.Replace(body, `"starting_soc":80,`, "", 1)
	}
	return body
}

// The demo has no planner: nothing under the trip routes is reachable there,
// whichever way it asks (the saved-trip comparison is the one exception).
func TestDemoCannotReachThePlannerRoutes(t *testing.T) {
	id := uuid.NewString()
	for _, path := range []string{
		"/api/trips/saved/" + id + "/active",
		"/api/trips/plan",
		"/api/trips/plan-multiday",
		"/api/trips/overnight-candidates",
	} {
		for _, method := range []string{"GET", "POST"} {
			if demoOnlyPublic(method, path, demoVeh) || demoAllowed(method, path, demoVeh) {
				t.Errorf("%s %s is reachable on the demo", method, path)
			}
		}
	}
}

func TestErrClassNeverCarriesTheText(t *testing.T) {
	secret := "gateway said: token abc123 for 34.05,-118.24"
	for name, err := range map[string]error{
		"plain":         errors.New(secret),
		"wrapped plain": fmt.Errorf("planTrip2: %w", errors.New(secret)),
		"upstream":      &rivian.UpstreamError{Class: rivian.ClassRateLimited, Reason: secret},
	} {
		if c := errClass(err); strings.Contains(c, "abc123") || strings.Contains(c, "34.05") || c == "" {
			t.Errorf("%s: class %q", name, c)
		}
	}
	if errClass(&rivian.UpstreamError{Class: rivian.ClassRateLimited}) != rivian.ClassRateLimited.String() {
		t.Error("the upstream class is what is logged")
	}
}
