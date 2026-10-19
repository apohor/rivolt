package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// gatewayGuard is a planner that fails the test if a departure the gateway
// layer would refuse (or a person would not pick) reaches it, and otherwise
// behaves like daysPlanner.
type gatewayGuard struct {
	t    *testing.T
	seen []time.Time // each departure as the gateway layer received it
	daysPlanner
}

func (g *gatewayGuard) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	if !in.DepartureTime.IsZero() {
		if !in.DepartureTime.After(testNow) || in.DepartureTime.After(testNow.Add(farFutureAfter)) {
			g.t.Errorf("a departure of %s reached the gateway layer (now %s)", in.DepartureTime, testNow)
		}
	}
	g.seen = append(g.seen, in.DepartureTime)
	if in.DepartureTime.IsZero() {
		in.DepartureTime = testNow // the gateway plans from its own "now"
	}
	return g.daysPlanner.PlanTrip(ctx, in)
}

func TestFarFutureShift(t *testing.T) {
	if _, on := farFutureShift(testNow.Add(29*24*time.Hour), testNow); on {
		t.Error("29 days ahead is shifted")
	}
	if _, on := farFutureShift(time.Time{}, testNow); on {
		t.Error("no start is shifted")
	}
	s, on := farFutureShift(testNow.Add(60*24*time.Hour), testNow)
	if !on || s != 60*24*time.Hour {
		t.Errorf("shift %v %v", s, on)
	}
}

func TestSingleDayFarFuturePlansNowAndAnswersInTheRequestedDates(t *testing.T) {
	depart := testNow.Add(60 * 24 * time.Hour).Truncate(time.Hour)
	gp := &gatewayGuard{t: t}
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: gp}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"depart_at":"`+depart.Format(time.RFC3339)+`",`))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		FarFuture bool `json:"far_future"`
		Routes    []struct {
			Waypoints []struct{ ArrivalTimeUTC, DepartureTimeUTC string }
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.FarFuture || len(gp.seen) != 1 {
		t.Fatalf("far_future %v, planner calls %d", out.FarFuture, len(gp.seen))
	}
	// The planner was asked for "now" (no departure), the answer is in the requested week.
	if !gp.seen[0].IsZero() {
		t.Errorf("the gateway layer was given departure %v, want none for day 1", gp.seen[0])
	}
	if len(out.Routes) == 0 || len(out.Routes[0].Waypoints) == 0 {
		t.Fatalf("no waypoints: %s", rec.Body)
	}
	last := out.Routes[0].Waypoints[len(out.Routes[0].Waypoints)-1]
	arr, err := time.Parse(time.RFC3339, last.ArrivalTimeUTC)
	if err != nil || arr.Before(depart) || arr.After(depart.Add(24*time.Hour)) {
		t.Errorf("arrival %v, want within a day after the requested departure %v", last.ArrivalTimeUTC, depart)
	}

	// Within 30 days nothing changes: the departure goes down as given, no flag.
	near := testNow.Add(10 * 24 * time.Hour).Truncate(time.Hour)
	gn := &gatewayGuard{t: t}
	rec = httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: gn}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(`"depart_at":"`+near.Format(time.RFC3339)+`",`))))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "far_future") || len(gn.seen) != 1 || !gn.seen[0].Equal(near) {
		t.Errorf("near trip: %d far_future in body %v departure %v", rec.Code, strings.Contains(rec.Body.String(), "far_future"), gn.seen)
	}
}

func TestMultidayFarFutureKeepsDaySpacingAndRequestedDates(t *testing.T) {
	// 2027-03-07 08:00 in Los Angeles is the day before the spring-forward change
	// (2027-03-14 is the change; use a start that crosses it): the second night's
	// 08:00 is a PDT time.
	la, _ := time.LoadLocation("America/Los_Angeles")
	start := time.Date(2027, 3, 13, 8, 0, 0, 0, la)
	gp := &gatewayGuard{t: t}
	code, out := runMultiday(t, gp, `{`+mdVehicle+`"start_at":"`+start.Format(time.RFC3339)+`","tz":"America/Los_Angeles",
	  "origin":{"lat":34.16,"lon":-118.17},
	  "days":[{"end":{"lat":36.0,"lon":-119.96},"overnight":{"l2_kw":0,"until":"08:00"}},
	          {"end":{"lat":37.79,"lon":-122.39},"overnight":{"l2_kw":0,"until":"08:00"}},
	          {"end":{"lat":34.16,"lon":-118.17}}]}`)
	if code != 200 || out["far_future"] != true {
		t.Fatalf("%d %v", code, out)
	}
	days := out["days"].([]any)
	if len(days) != 3 || len(gp.seen) != 3 {
		t.Fatalf("%d days, %d planner calls", len(days), len(gp.seen))
	}
	// Day 1 leaves at the start; the next two leave at 08:00 local, whichever the UTC offset (the change is in between).
	want := []time.Time{start, time.Date(2027, 3, 14, 8, 0, 0, 0, la), time.Date(2027, 3, 15, 8, 0, 0, 0, la)}
	for i, d := range days {
		dep, _ := time.Parse(time.RFC3339, d.(map[string]any)["depart_at"].(string))
		if !dep.Equal(want[i]) {
			t.Errorf("day %d departs %v, want %v", i+1, dep, want[i])
		}
	}
	// The gateway layer saw day 1 as "now" and the later days within a few days of it.
	if !gp.seen[0].IsZero() {
		t.Errorf("day 1 departure given: %v", gp.seen[0])
	}
	if gap := gp.seen[2].Sub(testNow); gap < 47*time.Hour || gap > 50*time.Hour {
		t.Errorf("day 3 shifted to %v after now, want about two days", gap)
	}
}

func TestFarFutureLimits(t *testing.T) {
	for name, body := range map[string]string{
		"181 days": `{` + mdVehicle + `"start_at":"` + testNow.Add(181*24*time.Hour).Format(time.RFC3339) + `","origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`,
		"a day leaves more than 30 days after the start": `{` + mdVehicle + `"start_at":"` + testNow.Add(60*24*time.Hour).Format(time.RFC3339) + `","origin":{"lat":1,"lon":1},` +
			`"days":[{"end":{"lat":2,"lon":2}},{"depart_at":"` + testNow.Add(100*24*time.Hour).Format(time.RFC3339) + `","end":{"lat":3,"lon":3}}]}`,
	} {
		p := &daysPlanner{}
		code, out := runMultiday(t, p, body)
		if code != 400 || len(p.calls) != 0 {
			t.Errorf("%s: %d %v (planner calls %d)", name, code, out["error"], len(p.calls))
		}
	}
	// 179 days is accepted.
	p := &gatewayGuard{t: t}
	if code, out := runMultiday(t, p, `{`+mdVehicle+`"start_at":"`+testNow.Add(179*24*time.Hour).Format(time.RFC3339)+`","origin":{"lat":1,"lon":1},"days":[{"end":{"lat":2,"lon":2}}]}`); code != 200 || out["far_future"] != true {
		t.Errorf("179 days: %d %v", code, out["error"])
	}
}
