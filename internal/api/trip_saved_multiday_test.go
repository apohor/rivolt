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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/trips"
)

// A saved 3-day trip: the plan itself, day by day, as plan-multiday returned it.
var mdSaved = &trips.SavedTrip{
	ID: uuid.New(), Name: "LA to SF and back",
	Inputs:    json.RawMessage(`{"origin":{"lat":34.16,"lon":-118.17}}`),
	CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	Plan: json.RawMessage(`{"kind":"multiday","days":[
	  {"index":1,"depart_at":"2026-10-20T15:00:00Z","start":{"latitude":34.16,"longitude":-118.17},"end":{"latitude":36.0,"longitude":-119.96},
	   "plan":{"Status":"OK","Routes":[{"ArrivalSoC":26}]},"costs":[{"currency":"USD","dcfc_spend_user_member":12.5}]},
	  {"index":2,"depart_at":"2026-10-21T15:00:00Z","start":{"latitude":36.0,"longitude":-119.96},"end":{"lat":37.79,"lon":-122.39},
	   "plan":{"Status":"OK","Routes":[{"ArrivalSoC":18}]}},
	  {"index":3,"depart_at":"2026-10-22T15:00:00Z","start":{"latitude":37.79,"longitude":-122.39},"end":{"latitude":34.16,"longitude":-118.17},
	   "plan":{"Status":"OK","Routes":[{"ArrivalSoC":15}]}}]}`),
}

func newMDPA(cands []ledger.PlanCandidate) (*planActualAPI, *fakePlanLinks, *[]string) {
	a, links, parsed := newFakePA(cands)
	a.getTrip = func(_ context.Context, _, id uuid.UUID) (*trips.SavedTrip, error) {
		if id == mdSaved.ID {
			return mdSaved, nil
		}
		if id == paSaved.ID {
			return paSaved, nil
		}
		return nil, trips.ErrNotFound
	}
	a.parse = func(inputs, plan json.RawMessage, _ time.Time) (ledger.Plan, error) {
		*parsed = append(*parsed, string(inputs)+" || "+string(plan))
		return ledger.Plan{}, nil
	}
	return a, links, parsed
}

func mdServe(a *planActualAPI, method, path, body string) (int, map[string]any) {
	r := chi.NewRouter()
	uid := uuid.New()
	r.Get("/trips/saved/{id}/actual", func(w http.ResponseWriter, req *http.Request) { a.actual(uid, w, req) })
	r.Post("/trips/saved/{id}/link", func(w http.ResponseWriter, req *http.Request) { a.link(uid, w, req) })
	r.Post("/trips/saved/{id}/unlink", func(w http.ResponseWriter, req *http.Request) { a.unlink(uid, w, req) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestMultidayActualIsOnePerDayAgainstThatDaysPlan(t *testing.T) {
	a, _, parsed := newMDPA(nil)
	code, out := mdServe(a, "GET", pathFor(mdSaved.ID, "actual"), "")
	if code != 200 || out["kind"] != "multiday" {
		t.Fatalf("%d %v", code, out)
	}
	days := out["days"].([]any)
	if len(days) != 3 {
		t.Fatalf("%d days", len(days))
	}
	for i, d := range days {
		m := d.(map[string]any)
		if m["day"] != float64(i+1) || m["status"] != "none" || m["saved_trip_id"] != mdSaved.ID.String() {
			t.Errorf("day %d = %v", i+1, m)
		}
	}
	// Each day was parsed from its own plan: its departure and endpoints as inputs,
	// its plan with the cost estimate alongside when it has one.
	if len(*parsed) != 3 {
		t.Fatalf("parsed %d plans", len(*parsed))
	}
	p1 := (*parsed)[0]
	for _, want := range []string{`"departure_at":"2026-10-20T15:00:00Z"`, `"origin":{"lat":34.16,"lon":-118.17}`, `"destination":{"lat":36,"lon":-119.96}`, `"ArrivalSoC":26`, `"costs":[{"currency":"USD","dcfc_spend_user_member":12.5}]`} {
		if !strings.Contains(p1, want) {
			t.Errorf("day 1 input lacks %s:\n%s", want, p1)
		}
	}
	if p2 := (*parsed)[1]; !strings.Contains(p2, `"destination":{"lat":37.79,"lon":-122.39}`) || strings.Contains(p2, "costs") {
		t.Errorf("day 2 (lat/lon spelling, no costs): %s", p2)
	}
	// ?day=2 is just that day.
	*parsed = nil
	code, out = mdServe(a, "GET", pathFor(mdSaved.ID, "actual")+"?day=2", "")
	if code != 200 || out["day"] != float64(2) || out["kind"] != nil || len(*parsed) != 1 || !strings.Contains((*parsed)[0], "2026-10-21T15:00:00Z") {
		t.Errorf("?day=2: %d %v parsed %v", code, out, *parsed)
	}
	for _, bad := range []string{"0", "4", "-1", "x", "1.5"} {
		if code, _ := mdServe(a, "GET", pathFor(mdSaved.ID, "actual")+"?day="+bad, ""); code != 400 {
			t.Errorf("?day=%s = %d, want 400", bad, code)
		}
	}
	// A single-day trip is unchanged, and refuses a day.
	code, one := mdServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if code != 200 || one["kind"] != nil || one["day"] != nil || one["status"] != "none" {
		t.Errorf("single-day: %d %v", code, one)
	}
	if code, _ := mdServe(a, "GET", pathFor(paSaved.ID, "actual")+"?day=1", ""); code != 400 {
		t.Errorf("a day on a single-day trip = %d, want 400", code)
	}
}

func TestMultidayLinksAreEachDays(t *testing.T) {
	a, links, _ := newMDPA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high")})
	// A multi-day trip needs the day.
	if code, _ := mdServe(a, "POST", pathFor(mdSaved.ID, "link"), `{"trip_id":"trip_a"}`); code != 400 {
		t.Errorf("link without a day = %d, want 400", code)
	}
	if code, _ := mdServe(a, "POST", pathFor(mdSaved.ID, "link"), `{"trip_id":"trip_a","day":4}`); code != 400 {
		t.Errorf("link day 4 of 3 = %d, want 400", code)
	}
	// One recorded outing carries the whole trip, so every day links to it.
	for _, day := range []int{1, 2, 3} {
		code, out := mdServe(a, "POST", pathFor(mdSaved.ID, "link"), `{"trip_id":"trip_a","day":`+string(rune('0'+day))+`}`)
		if code != 200 || out["status"] != "linked" || out["day"] != float64(day) {
			t.Fatalf("link day %d: %d %v", day, code, out)
		}
	}
	if len(links.rows) != 3 {
		t.Fatalf("rows = %+v", links.rows)
	}
	for i, r := range links.rows {
		// Each link snapshots its own day's plan, not the whole trip.
		if r.Day != i+1 || r.Status != "linked" || strings.Contains(string(r.Snapshot.Plan), "kind") || !strings.Contains(string(r.Snapshot.Inputs), "departure_at") {
			t.Errorf("row %d = %+v", i, r)
		}
	}
	// Unlinking day 2 leaves the others.
	code, out := mdServe(a, "POST", pathFor(mdSaved.ID, "unlink"), `{"day":2}`)
	if code != 200 || out["status"] == "linked" {
		t.Fatalf("unlink day 2: %d %v", code, out)
	}
	got := map[int]string{}
	for _, r := range links.rows {
		got[r.Day] = r.Status
	}
	if got[1] != "linked" || got[2] != "unlinked" || got[3] != "linked" {
		t.Errorf("after unlinking day 2: %v", got)
	}
	// And the multi-day view reports each day by itself.
	_, all := mdServe(a, "GET", pathFor(mdSaved.ID, "actual"), "")
	st := []string{}
	for _, d := range all["days"].([]any) {
		st = append(st, d.(map[string]any)["status"].(string))
	}
	if st[0] != "linked" || st[1] == "linked" || st[2] != "linked" {
		t.Errorf("statuses = %v", st)
	}
	// A single-day trip still links with no day, and refuses one.
	if code, _ := mdServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_a"}`); code != 200 {
		t.Errorf("single-day link = %d", code)
	}
	if code, _ := mdServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_a","day":1}`); code != 400 {
		t.Errorf("single-day link with a day = %d, want 400", code)
	}
}

func TestValidateSavedPlan(t *testing.T) {
	day := `{"plan":{"Status":"OK"},"start":{"latitude":1,"longitude":2},"end":{"latitude":3,"longitude":4}}`
	many := strings.TrimSuffix(strings.Repeat(day+",", 11), ",")
	for name, tc := range map[string]struct {
		plan string
		ok   bool
	}{
		"single-day plan":     {`{"Status":"OK","Routes":[]}`, true},
		"no plan":             {``, true},
		"null":                {`null`, true},
		"an array":            {`[1]`, true},
		"multiday, one day":   {`{"kind":"multiday","days":[` + day + `]}`, true},
		"multiday, ten days":  {`{"kind":"multiday","days":[` + strings.TrimSuffix(strings.Repeat(day+",", 10), ",") + `]}`, true},
		"multiday, eleven":    {`{"kind":"multiday","days":[` + many + `]}`, false},
		"multiday, no days":   {`{"kind":"multiday","days":[]}`, false},
		"multiday, no days k": {`{"kind":"multiday"}`, false},
		"day without a plan":  {`{"kind":"multiday","days":[{"start":{"latitude":1,"longitude":2}}]}`, false},
		"day plan not object": {`{"kind":"multiday","days":[{"plan":[1]}]}`, false},
		"bad latitude":        {`{"kind":"multiday","days":[{"plan":{},"end":{"latitude":95,"longitude":2}}]}`, false},
		"bad longitude":       {`{"kind":"multiday","days":[{"plan":{},"start":{"lat":1,"lon":200}}]}`, false},
		"day not an object":   {`{"kind":"multiday","days":[7]}`, false},
	} {
		err := validateSavedPlan(json.RawMessage(tc.plan))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}
