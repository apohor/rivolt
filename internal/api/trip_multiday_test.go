package api

import (
	"bytes"
	"context"
	"encoding/json"
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

// daysPlanner plans each call as a fixed 3 h drive from the requested
// departure, arriving with 40 %, and records every input. A call listed in
// fail errors.
type daysPlanner struct {
	calls []rivian.PlanTripInput
	fail  map[int]error
	sim   bool
}

func (p *daysPlanner) PlanTrip(_ context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	i := len(p.calls)
	p.calls = append(p.calls, in)
	if err := p.fail[i]; err != nil {
		return nil, err
	}
	var wps []rivian.PlannedWaypoint
	for j, w := range in.Waypoints {
		pw := rivian.PlannedWaypoint{Latitude: w.Latitude, Longitude: w.Longitude, WaypointType: strings.ToUpper(w.WaypointType)}
		frac := time.Duration(j) * 3 * time.Hour / time.Duration(len(in.Waypoints)-1)
		pw.ArrivalTimeUTC = in.DepartureTime.Add(frac).UTC().Format(time.RFC3339)
		pw.DepartureTimeUTC = pw.ArrivalTimeUTC
		wps = append(wps, pw)
	}
	return &rivian.TripPlan{Status: "OK", Simulated: p.sim, Routes: []rivian.TripRoute{{
		DestinationReached: true, ArrivalSoC: 40, TotalDriveDurationSec: 3 * 3600, TotalTripDurationSec: 3 * 3600,
		TotalDriveDistanceMeters: 250_000, Waypoints: wps,
	}}}, nil
}

func runMultiday(t *testing.T, p rivian.TripPlanner, body string) (int, map[string]any) {
	t.Helper()
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: p}
	rec := httptest.NewRecorder()
	handleTripPlanMultiday(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan-multiday", bytes.NewBufferString(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{}
	}
	out["_body"] = rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); rec.Code >= 400 && !strings.HasPrefix(ct, "application/json") {
		t.Errorf("an error response must be JSON, got %q: %s", ct, rec.Body.String())
	}
	return rec.Code, out
}

const mdVehicle = `"vehicle_id":"v1","starting_soc":90,"pack_kwh":131,`

func TestMultidayDayModel(t *testing.T) {
	p := &daysPlanner{sim: true}
	code, out := runMultiday(t, p, `{`+mdVehicle+`
	  "start_at":"2026-10-20T08:00:00-07:00","tz":"America/Los_Angeles",
	  "origin":{"lat":34.16,"lon":-118.17,"label":"Home"},
	  "days":[
	   {"end":{"lat":36.0,"lon":-119.96,"label":"Kettleman"},"target_arrival_soc_pct":10,
	    "overnight":{"l2_kw":3.3,"until":"08:00","max_soc_pct":80}},
	   {"vias":[{"lat":36.5,"lon":-120.5,"label":"Via"}],"end":{"lat":37.79,"lon":-122.39,"label":"SF"},
	    "overnight":{"l2_kw":0,"until":"08:00"}},
	   {"end":{"lat":34.16,"lon":-118.17,"label":"Home"}}]}`)
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	if out["simulated"] != true || out["failed_day"] != nil {
		t.Errorf("simulated %v failed_day %v", out["simulated"], out["failed_day"])
	}
	days := out["days"].([]any)
	if len(days) != 3 {
		t.Fatalf("%d days", len(days))
	}
	d1, d2, d3 := days[0].(map[string]any), days[1].(map[string]any), days[2].(map[string]any)

	// Each day leaves at its own time: day 1 at start_at, the rest at the next local 08:00.
	if d1["depart_at"] != "2026-10-20T15:00:00Z" || d1["arrive_at"] != "2026-10-20T18:00:00Z" {
		t.Errorf("day 1 %v -> %v", d1["depart_at"], d1["arrive_at"])
	}
	if d2["depart_at"] != "2026-10-21T15:00:00Z" || d3["depart_at"] != "2026-10-22T15:00:00Z" {
		t.Errorf("departures %v / %v (08:00 PDT is 15:00Z)", d2["depart_at"], d3["depart_at"])
	}
	// Overnight 1: 3.3 kW for 21 h at 90 %: 62.4 kWh = +47.6 points from 40 -> 87.6, over the 80 cap.
	ov := d1["overnight"].(map[string]any)
	if ov["parked_hours"] != float64(21) || ov["l2_kw"] != 3.3 || ov["capped"] != true || ov["post_charge_soc_pct"] != float64(80) {
		t.Errorf("overnight 1 = %v", ov)
	}
	if d2["departure_soc"] != float64(80) {
		t.Errorf("day 2 starts at %v, want overnight 1's 80", d2["departure_soc"])
	}
	// Overnight 2 is explicitly no L2: the next day starts with what day 2 arrived with.
	ov2 := d2["overnight"].(map[string]any)
	if ov2["added_kwh"] != float64(0) || d3["departure_soc"] != float64(40) {
		t.Errorf("overnight 2 = %v, day 3 starts at %v", ov2, d3["departure_soc"])
	}
	// Vias are kept, in order, as waypoints of that day's plan only.
	if len(p.calls) != 3 || len(p.calls[0].Waypoints) != 2 || len(p.calls[1].Waypoints) != 3 || p.calls[1].Waypoints[1].Latitude != 36.5 || p.calls[1].Waypoints[1].WaypointType != "waypoint" {
		t.Errorf("planner calls = %+v", p.calls)
	}
	// Day 1's target applies to day 1 only.
	if p.calls[0].TargetArrivalSocPercent == nil || *p.calls[0].TargetArrivalSocPercent != 10 || p.calls[1].TargetArrivalSocPercent != nil {
		t.Error("per-day target not applied as given")
	}
	// The day echoes its endpoints for the SPA.
	if d2["start"].(map[string]any)["label"] != "Kettleman" || d2["end"].(map[string]any)["label"] != "SF" || len(d2["vias"].([]any)) != 1 {
		t.Errorf("day 2 endpoints = %v / %v / %v", d2["start"], d2["vias"], d2["end"])
	}

	// Determinism: the same request twice gives byte-identical JSON.
	body := `{` + mdVehicle + `"start_at":"2026-10-20T08:00:00Z","origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"overnight":{"until":"08:00"}},{"end":{"lat":3,"lon":3}}]}`
	run := func() string {
		c := plannerClient{Client: rivian.NewStub(), TripPlanner: &daysPlanner{}}
		rec := httptest.NewRecorder()
		handleTripPlanMultiday(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(body)))
		return rec.Body.String()
	}
	if a, b := run(), run(); a != b || a == "" {
		t.Error("the same request must give the same JSON")
	}
}

func TestMultidayLegacyShapeStillWorks(t *testing.T) {
	p := &daysPlanner{}
	code, out := runMultiday(t, p, `{`+mdVehicle+`"start_at":"2026-10-20T08:00:00Z",
	  "origin":{"latitude":34,"longitude":-118},"destination":{"latitude":36,"longitude":-115},
	  "overnights":[{"latitude":35,"longitude":-117,"name":"Hotel","parked_hours":10,"l2_kw":7}],
	  "max_overnight_soc_pct":70}`)
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	days := out["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("%d days", len(days))
	}
	ov := days[0].(map[string]any)["overnight"].(map[string]any)
	// 7 kW for the fixed 10 h: 63 kWh offered, capped at 70 %.
	if ov["name"] != "Hotel" || ov["parked_hours"] != float64(10) || ov["post_charge_soc_pct"] != float64(70) || ov["capped"] != true {
		t.Errorf("overnight = %v", ov)
	}
	if p.calls[0].Waypoints[0].Latitude != 34 || p.calls[1].Waypoints[1].Latitude != 36 {
		t.Errorf("legacy coordinates not mapped: %+v", p.calls)
	}
	// A legacy overnight with no l2_kw is "no charging", as it always was.
	p2 := &daysPlanner{}
	_, out2 := runMultiday(t, p2, `{`+mdVehicle+`"start_at":"2026-10-20T08:00:00Z","origin":{"latitude":34,"longitude":-118},
	  "destination":{"latitude":36,"longitude":-115},"overnights":[{"latitude":35,"longitude":-117}]}`)
	ov2 := out2["days"].([]any)[0].(map[string]any)["overnight"].(map[string]any)
	if ov2["l2_kw"] != float64(0) || ov2["added_kwh"] != float64(0) {
		t.Errorf("legacy overnight without l2_kw = %v", ov2)
	}
}

func TestMultidayRefusals(t *testing.T) {
	day := `{"end":{"lat":2,"lon":2}}`
	manyVias := strings.Repeat(`{"lat":5,"lon":5},`, 8) + `{"lat":5,"lon":5}` // 9 vias + start + end = 11
	elevenDays := strings.TrimSuffix(strings.Repeat(day+",", 11), ",")
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"eleven waypoints on day 2": {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[` + day + `,{"vias":[` + manyVias + `],"end":{"lat":3,"lon":3}}]}`, "day 2"},
		"eleven days":               {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[` + elevenDays + `]}`, "11 days"},
		"no days":                   {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[]}`, "at least one day"},
		"bad start_at":              {`{` + mdVehicle + `"start_at":"tomorrow","origin":{"lat":1,"lon":1},"days":[` + day + `]}`, "start_at"},
		"bad depart_at":             {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"depart_at":"soon"}]}`, "day 1"},
		"bad until":                 {`{` + mdVehicle + `"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"overnight":{"until":"8am"}},` + day + `]}`, "day 1"},
		"no pack":                   {`{"vehicle_id":"v1","starting_soc":90,"origin":{"lat":1,"lon":1},"days":[` + day + `]}`, "pack_kwh"},
		"no vehicle":                {`{"starting_soc":90,"pack_kwh":131,"origin":{"lat":1,"lon":1},"days":[` + day + `]}`, "vehicle_id"},
		"no soc":                    {`{"vehicle_id":"v1","pack_kwh":131,"origin":{"lat":1,"lon":1},"days":[` + day + `]}`, "starting_soc"},
	} {
		p := &daysPlanner{}
		code, out := runMultiday(t, p, tc.body)
		body, _ := out["_body"].(string)
		if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d %q, want 400 mentioning %q", name, code, body, tc.want)
		}
		if len(p.calls) != 0 {
			t.Errorf("%s: the planner was called", name)
		}
	}
}

func TestMultidayPartialFailureIs200WithTheDaysSoFar(t *testing.T) {
	p := &daysPlanner{fail: map[int]error{1: &rivian.UpstreamError{Class: rivian.ClassRateLimited, Reason: "slow down"}}}
	code, out := runMultiday(t, p, `{`+mdVehicle+`"start_at":"2026-10-20T08:00:00Z","origin":{"lat":1,"lon":1},
	  "days":[{"end":{"lat":2,"lon":2},"overnight":{"l2_kw":0}},{"end":{"lat":3,"lon":3},"overnight":{}},{"end":{"lat":4,"lon":4}}]}`)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(out["days"].([]any)) != 1 || out["failed_day"] != float64(2) {
		t.Errorf("days %d, failed_day %v", len(out["days"].([]any)), out["failed_day"])
	}
	if out["error_class"] != rivian.ClassRateLimited.String() || out["error_reason"] != "slow down" || !strings.Contains(out["error"].(string), "day 2") {
		t.Errorf("error fields = %v / %v / %v", out["error"], out["error_class"], out["error_reason"])
	}
	if len(p.calls) != 2 {
		t.Errorf("chaining must stop at the failed day, planner called %d times", len(p.calls))
	}
	// A plain error has no class.
	_, out = runMultiday(t, &daysPlanner{fail: map[int]error{0: errors.New("boom")}}, `{`+mdVehicle+`"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`)
	if out["failed_day"] != float64(1) || out["error_class"] != nil || len(out["days"].([]any)) != 0 {
		t.Errorf("first-day failure = %v", out)
	}
}

// Every error from the plan endpoints has one shape: JSON {"error": "..."}.
func TestPlanErrorsAreAlwaysJSON(t *testing.T) {
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: &daysPlanner{}}
	for name, req := range map[string]struct {
		h    http.HandlerFunc
		body string
		want int
	}{
		"single-day, one waypoint": {handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil), `{"waypoints":[{"latitude":1,"longitude":1}]}`, 400},
		"single-day, bad json":     {handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil), `{`, 400},
		"single-day, stub client":  {handleTripPlan(rivian.NewStub(), nil, nil, uuid.New(), nil, nil, nil, nil), `{}`, 404},
		"multi-day, bad json":      {handleTripPlanMultiday(c, nil, nil, uuid.New(), nil, nil, nil, nil), `{`, 400},
		"multi-day, stub client":   {handleTripPlanMultiday(rivian.NewStub(), nil, nil, uuid.New(), nil, nil, nil, nil), `{}`, 404},
	} {
		rec := httptest.NewRecorder()
		req.h(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(req.body)))
		var out map[string]any
		if rec.Code != req.want || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
			json.Unmarshal(rec.Body.Bytes(), &out) != nil || out["error"] == "" || out["error"] == nil {
			t.Errorf("%s: %d %q %s", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

// A missing or unrecognised zone is not an error: "until" falls back to UTC
// and the response says what was used.
func TestMultidayTimezoneFallsBackToUTC(t *testing.T) {
	body := func(tz string) string {
		return `{` + mdVehicle + tz + `"start_at":"2026-10-20T08:00:00-07:00","origin":{"lat":1,"lon":1},
		 "days":[{"end":{"lat":2,"lon":2},"overnight":{"until":"08:00","l2_kw":0}},{"end":{"lat":3,"lon":3}}]}`
	}
	for name, tc := range map[string]struct{ tz, used, day2 string }{
		"los angeles": {`"tz":"America/Los_Angeles",`, "America/Los_Angeles", "2026-10-21T15:00:00Z"},
		"chicago":     {`"tz":"America/Chicago",`, "America/Chicago", "2026-10-21T13:00:00Z"},
		"missing":     {``, "UTC", "2026-10-21T08:00:00Z"},
		"invalid":     {`"tz":"Mars/Base",`, "UTC", "2026-10-21T08:00:00Z"},
		"garbage":     {`"tz":"../../etc/passwd",`, "UTC", "2026-10-21T08:00:00Z"},
	} {
		code, out := runMultiday(t, &daysPlanner{}, body(tc.tz))
		if code != 200 || out["tz"] != tc.used {
			t.Errorf("%s: %d tz %v, want 200 and %q", name, code, out["tz"], tc.used)
			continue
		}
		if d2 := out["days"].([]any)[1].(map[string]any)["depart_at"]; d2 != tc.day2 {
			t.Errorf("%s: day 2 departs %v, want %v", name, d2, tc.day2)
		}
	}
}

// A failed day's message is for the person planning: no router, gateway or
// package names. The detail stays in the log.
func TestFailedDayMessageHasNoInternals(t *testing.T) {
	body := `{` + mdVehicle + `"start_at":"2026-10-20T08:00:00Z","origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"router 400":    {errors.New("syntheticplan: route: valhalla: HTTP 400"), "the planner could not plan this day"},
		"gateway":       {errors.New("planTrip2: graphql: boom {\"x\":1}"), "the planner could not plan this day"},
		"no road route": {fmt.Errorf("%w: no road route between these points", rivian.ErrInvalidTrip), "no road route between these points"},
		"rate limited":  {&rivian.UpstreamError{Class: rivian.ClassRateLimited, Reason: "429 raw"}, "the planner is busy"},
		"reauth":        {rivian.ErrNeedsReauth, "reconnect your Rivian account"},
		"paused":        {rivian.ErrUpstreamPaused, "paused"},
	} {
		_, out := runMultiday(t, &daysPlanner{fail: map[int]error{0: tc.err}}, body)
		msg, _ := out["error"].(string)
		if out["failed_day"] != float64(1) || !strings.Contains(msg, "day 1 could not be planned: ") || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: failed_day %v error %q, want it to say %q", name, out["failed_day"], msg, tc.want)
		}
		for _, internal := range []string{"syntheticplan", "valhalla", "HTTP 400", "planTrip2", "graphql", "invalid trip", "429 raw"} {
			if strings.Contains(msg, internal) {
				t.Errorf("%s: the message leaks %q: %s", name, internal, msg)
			}
		}
	}
}

func TestMultidayRoutePreferencesGoToEveryDay(t *testing.T) {
	p := &daysPlanner{}
	code, _ := runMultiday(t, p, `{`+mdVehicle+`"start_at":"2026-10-20T08:00:00Z","route_preferences":{"avoid_ferries":true},
	  "origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2},"overnight":{"l2_kw":0}},{"end":{"lat":3,"lon":3}}]}`)
	if code != 200 || len(p.calls) != 2 {
		t.Fatalf("status %d, calls %d", code, len(p.calls))
	}
	for i, c := range p.calls {
		if c.RoutePreferences == nil || !c.RoutePreferences.AvoidFerries || c.RoutePreferences.AvoidTolls {
			t.Errorf("day %d preferences = %+v", i+1, c.RoutePreferences)
		}
	}
	p2 := &daysPlanner{}
	runMultiday(t, p2, `{`+mdVehicle+`"origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`)
	if p2.calls[0].RoutePreferences != nil {
		t.Error("no preference must leave the input unset")
	}
}
