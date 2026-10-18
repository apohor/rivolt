package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/trips"
)

func savedWith(plan string) *trips.SavedTrip {
	return &trips.SavedTrip{ID: uuid.New(), Plan: json.RawMessage(plan)}
}

// Three days: leave 08:00 each morning, the last arriving 14:00 on day 3.
const threeDays = `{"kind":"multiday","days":[
 {"depart_at":"2026-10-20T15:00:00Z","arrive_at":"2026-10-20T21:00:00Z","plan":{}},
 {"depart_at":"2026-10-21T15:00:00Z","arrive_at":"2026-10-21T22:00:00Z","plan":{}},
 {"depart_at":"2026-10-22T15:00:00Z","arrive_at":"2026-10-22T21:00:00Z","plan":{}}]}`

func TestActiveAtPicksTheDayNowFallsIn(t *testing.T) {
	st := savedWith(threeDays)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for name, tc := range map[string]struct {
		now       string
		state     string
		day       int // 0 = none
		remaining int
	}{
		"before the trip":           {"2026-10-19T09:00:00Z", "before", 0, 3},
		"just before day 1":         {"2026-10-20T14:59:59Z", "before", 0, 3},
		"day 1 departure":           {"2026-10-20T15:00:00Z", "active", 1, 3},
		"day 1 evening":             {"2026-10-20T23:00:00Z", "active", 1, 3},
		"the night between 1 and 2": {"2026-10-21T10:00:00Z", "active", 1, 3},
		"day 2 departure":           {"2026-10-21T15:00:00Z", "active", 2, 2},
		"day 3 afternoon":           {"2026-10-22T20:00:00Z", "active", 3, 1},
		"day 3 evening, late":       {"2026-10-23T05:00:00Z", "active", 3, 1}, // within 12 h of arrival
		"long after":                {"2026-10-24T00:00:00Z", "after", 0, 0},
	} {
		got := activeAt(st, at(tc.now), nil, 0)
		cur := 0
		if got.CurrentDay != nil {
			cur = *got.CurrentDay
		}
		if got.State != tc.state || cur != tc.day || got.RemainingDays != tc.remaining || got.Kind != "multiday" || got.DayCount != 3 {
			t.Errorf("%s: %+v (day %d), want %s day %d remaining %d", name, got, cur, tc.state, tc.day, tc.remaining)
		}
	}
	// The truck's position rides along (nil when there is none).
	pos := &fromPosition{Lat: 35, Lon: -119, SoC: 50}
	if got := activeAt(st, at("2026-10-21T16:00:00Z"), pos, 0); got.Position != pos {
		t.Error("the position should be echoed")
	}
	if got := activeAt(st, at("2026-10-21T16:00:00Z"), nil, 0); got.Position != nil {
		t.Error("no reading, no position")
	}
}

func TestActiveAtForOtherTrips(t *testing.T) {
	now := time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC)
	for name, plan := range map[string]string{
		"single-day":        `{"Status":"OK","Routes":[]}`,
		"no plan":           ``,
		"multiday, no days": `{"kind":"multiday","days":[]}`,
		"unparseable times": `{"kind":"multiday","days":[{"depart_at":"soon","plan":{}}]}`,
	} {
		got := activeAt(savedWith(plan), now, nil, 0)
		if got.CurrentDay != nil || got.State != "none" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := activeAt(savedWith(`{"Status":"OK"}`), now, nil, 0); got.Kind != "single" || got.DayCount != 0 {
		t.Errorf("single-day: %+v", got)
	}
	// A multi-day trip whose times are unreadable still says it is multi-day.
	if got := activeAt(savedWith(`{"kind":"multiday","days":[{"depart_at":"soon","plan":{}}]}`), now, nil, 0); got.Kind != "multiday" || got.DayCount != 1 {
		t.Errorf("unreadable times: %+v", got)
	}
}

const withStops = `{"kind":"multiday","days":[
 {"depart_at":"2026-10-20T15:00:00Z","arrive_at":"2026-10-20T21:00:00Z","plan":{},
  "start":{"lat":34.0,"lon":-118.0},"end":{"lat":36.0,"lon":-119.0},
  "vias":[{"lat":34.5,"lon":-118.2},{"lat":35.5,"lon":-118.8}],
  "overnight":{"l2_kw":7,"max_soc_pct":80}},
 {"depart_at":"2026-10-21T15:00:00Z","arrive_at":"2026-10-21T22:00:00Z","plan":{},
  "start":{"lat":36.0,"lon":-119.0},"end":{"lat":37.0,"lon":-120.0}}]}`

func TestActiveOvernightAndViasPassed(t *testing.T) {
	st := savedWith(withStops)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }

	got := activeAt(st, at("2026-10-21T04:00:00Z"), &fromPosition{Lat: 36.001, Lon: -119.0, SoC: 30}, 131)
	if got.Phase != "overnight" || got.NextDay == nil || *got.NextDay != 2 || got.NextDepartAt != "2026-10-21T15:00:00Z" {
		t.Fatalf("overnight: %+v", got)
	}
	if got.ProjectedSoC == nil || *got.ProjectedSoC <= 30 || *got.ProjectedSoC > 80 {
		t.Errorf("projected soc %v: expected a gain from 30, capped at 80", got.ProjectedSoC)
	}

	got = activeAt(st, at("2026-10-20T17:00:00Z"), &fromPosition{Lat: 35.0, Lon: -118.5, SoC: 60}, 131)
	if got.Phase != "driving" || len(got.ViasPassed) != 1 || got.ViasPassed[0] != 1 {
		t.Errorf("driving: %+v", got)
	}

	if got = activeAt(st, at("2026-10-20T18:00:00Z"), &fromPosition{Lat: 36.0, Lon: -119.0, SoC: 20}, 131); got.Phase != "driving" {
		t.Errorf("before arrive_at: %+v", got)
	}
	if got = activeAt(st, at("2026-10-21T04:00:00Z"), nil, 131); got.Phase != "" || got.NextDay != nil {
		t.Errorf("no position: %+v", got)
	}
}

func TestActiveForeignVehicleIs400AndLeaksNothing(t *testing.T) {
	withUserVehicles(t, "v1")
	calls := fixedPosition(t, truckAt, true)
	h := handleSavedTripActive(trips.NewFactory(nil), func(uuid.UUID) *rivian.StateMonitor { return nil }, &sql.DB{})
	r := chi.NewRouter()
	r.Get("/trips/saved/{id}/active", func(w http.ResponseWriter, req *http.Request) { h(uuid.New(), w, req) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trips/saved/"+uuid.NewString()+"/active?vehicle_id=someone-elses", nil))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unknown vehicle") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if *calls != 0 || strings.Contains(rec.Body.String(), "35.37") || strings.Contains(rec.Body.String(), "position") {
		t.Errorf("the position was read (%d calls) or leaked: %s", *calls, rec.Body.String())
	}
}
