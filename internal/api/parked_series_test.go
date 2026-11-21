package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func pr(at time.Time, soc float64, kind string, sleep bool) parkedRow {
	s, rng := soc, soc*3
	return parkedRow{At: at, SoC: &s, RangeMi: &rng, Kind: kind, Sleep: sleep}
}

// An overnight truck: parked 12 h, three sleeps and two wake-ups, then asleep now.
func parkedNight(now time.Time) []parkedRow {
	t0 := now.Add(-12 * time.Hour)
	at := func(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }
	return []parkedRow{
		pr(at(0), 80, "p", false),
		pr(at(0.5), 79.8, "p", true),
		pr(at(4), 79.5, "p", true),
		pr(at(4.2), 79.0, "p", false), // wake-up 1
		pr(at(4.4), 78.9, "p", false),
		pr(at(4.5), 78.9, "p", true),
		pr(at(8), 78.5, "p", true),
		pr(at(8.1), 78.0, "p", false), // wake-up 2
		pr(at(8.3), 77.6, "p", false),
		pr(at(8.4), 77.6, "p", true),
		pr(at(12), 77.0, "p", true),
	}
}

func TestBuildParkedSeriesOvernight(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	out := buildParkedSeries(parkedNight(now), now, time.UTC)
	if len(out.Points) != 11 || !out.Parked {
		t.Fatalf("points %d parked %v", len(out.Points), out.Parked)
	}
	if out.ParkedSince != nil {
		t.Fatalf("parked since is only stated when a drive precedes it: %v", out.ParkedSince)
	}
	if out.ChangePct == nil || *out.ChangePct != -3 {
		t.Fatalf("change %v", out.ChangePct)
	}
	if len(out.Sleeps) != 3 || !out.Sleeps[2].Open || out.Sleeps[0].Open {
		t.Fatalf("sleeps %+v", out.Sleeps)
	}
	if len(out.Wakes) != 2 || out.Wakes[0].SoCBefore != 79.5 || out.Wakes[0].SoCAfter != 79.0 {
		t.Fatalf("wakes %+v", out.Wakes)
	}
	f := out.Figures
	// Wake costs: 79.0 -> 78.9 asleep again = 0.1; 78.0 -> 77.6 = 0.4.
	if f.WakeUps != 2 || f.WakeCostPct == nil || *f.WakeCostPct < 0.249 || *f.WakeCostPct > 0.251 {
		t.Fatalf("figures %+v", f)
	}
	if f.IdlePctPerDay == nil || *f.IdlePctPerDay != 6 { // 3% over 12 h
		t.Fatalf("rate %v", f.IdlePctPerDay)
	}
	if f.IdleMiPerDay == nil || *f.IdleMiPerDay != 18 { // range is 3 mi per percent
		t.Fatalf("mi %v", f.IdleMiPerDay)
	}
	if out.Projection == nil || out.Projection.At.Hour() != 8 || out.Projection.SoC >= 77 {
		t.Fatalf("projection %+v", out.Projection)
	}
	if got := out.Projection.At.Sub(now); got < parkedSeriesProjectMinAhead {
		t.Fatalf("projection too soon: %v", got)
	}
	if want := int64((3.7 + 3.6 + 3.6) * 3600); f.AsleepTodayS != want {
		t.Fatalf("asleep today %d, want %d", f.AsleepTodayS, want)
	}
}

func TestBuildParkedSeriesFewMinutesNoRate(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	rows := []parkedRow{pr(now.Add(-2*time.Hour), 70, "d", false), pr(now.Add(-70*time.Minute), 70, "p", false),
		pr(now.Add(-60*time.Minute), 69.9, "p", false), pr(now.Add(-30*time.Minute), 69.8, "p", false), pr(now, 69.8, "p", false)}
	out := buildParkedSeries(rows, now, time.UTC)
	if len(out.Points) != 4 || out.ParkedSince == nil {
		t.Fatalf("%+v", out)
	}
	if out.Figures.IdlePctPerDay != nil || out.Projection != nil {
		t.Fatalf("no rate or projection before %v h parked: %+v", parkedSeriesMinRateH, out.Figures)
	}
}

func TestBuildParkedSeriesSparseShowsNothing(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	for name, rows := range map[string][]parkedRow{
		"none":      nil,
		"few":       {pr(now.Add(-5*time.Hour), 70, "p", false), pr(now, 69, "p", false)},
		"too_short": {pr(now.Add(-30*time.Minute), 70, "p", false), pr(now.Add(-20*time.Minute), 70, "p", false), pr(now.Add(-10*time.Minute), 70, "p", false), pr(now, 70, "p", false)},
	} {
		out := buildParkedSeries(rows, now, time.UTC)
		if len(out.Points) != 0 || out.Figures != nil || out.Sleeps == nil || out.Points == nil {
			t.Errorf("%s: %+v", name, out)
		}
	}
}

func TestBuildParkedSeriesNeverSlept(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	var rows []parkedRow
	for i := 0; i <= 8; i++ {
		rows = append(rows, pr(now.Add(-time.Duration(8-i)*time.Hour), 80-float64(i)*0.5, "p", false))
	}
	out := buildParkedSeries(rows, now, time.UTC)
	if len(out.Sleeps) != 0 || len(out.Wakes) != 0 || out.Figures.WakeUps != 0 || out.Figures.WakeCostPct != nil || out.Figures.AsleepTodayS != 0 {
		t.Fatalf("%+v", out)
	}
	if out.Figures.IdlePctPerDay == nil || *out.Figures.IdlePctPerDay != 12 {
		t.Fatalf("rate %v", out.Figures.IdlePctPerDay)
	}
}

func TestBuildParkedSeriesChargeStartsNewSegment(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	rows := []parkedRow{pr(now.Add(-10*time.Hour), 50, "p", false), pr(now.Add(-9*time.Hour), 49, "p", false),
		pr(now.Add(-8*time.Hour), 60, "c", false), pr(now.Add(-6*time.Hour), 80, "p", false), pr(now.Add(-4*time.Hour), 79, "p", false), pr(now.Add(-2*time.Hour), 78.5, "p", false), pr(now, 78, "p", false)}
	out := buildParkedSeries(rows, now, time.UTC)
	if len(out.Points) != 6 || out.Points[1].Seg != 0 || out.Points[2].Seg != 1 {
		t.Fatalf("%+v", out.Points)
	}
	if out.ParkedSince == nil || !out.ParkedSince.Equal(now.Add(-6*time.Hour)) || *out.ChangePct != -2 {
		t.Fatalf("since %v change %v", out.ParkedSince, out.ChangePct)
	}
	// The charge's gain is not "while parked": the rate and the projection count only the run after it (2 points over 6 h = 8 a day).
	if out.Figures.IdlePctPerDay == nil || math.Abs(*out.Figures.IdlePctPerDay-8) > 1e-9 {
		t.Fatalf("rate %v", out.Figures.IdlePctPerDay)
	}
	if out.Projection == nil || out.Projection.SoC >= 78 || out.Projection.SoC < 70 {
		t.Fatalf("projection %+v", out.Projection)
	}
}

// A sleep ends where the drive or the charge begins; it never runs across one.
func TestBuildParkedSeriesSleepStopsAtADriveAndAtACharge(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	rows := []parkedRow{pr(now.Add(-12*time.Hour), 80, "p", true), pr(now.Add(-11*time.Hour), 79.8, "p", true),
		pr(now.Add(-10*time.Hour), 79, "d", false), pr(now.Add(-9*time.Hour), 70, "p", false),
		pr(now.Add(-8*time.Hour), 69.9, "p", true), pr(now.Add(-7*time.Hour), 69.8, "p", true),
		pr(now.Add(-6*time.Hour), 70, "c", false), pr(now.Add(-4*time.Hour), 90, "p", false), pr(now, 89, "p", false)}
	out := buildParkedSeries(rows, now, time.UTC)
	if len(out.Sleeps) != 2 || !out.Sleeps[0].To.Equal(now.Add(-10*time.Hour)) || !out.Sleeps[1].To.Equal(now.Add(-6*time.Hour)) {
		t.Fatalf("%+v", out.Sleeps)
	}
	if len(out.Wakes) != 0 {
		t.Fatalf("a sleep ended by a drive or a charge is not a wake-up: %+v", out.Wakes)
	}
	if out.ParkedSince == nil || !out.ParkedSince.Equal(now.Add(-4*time.Hour)) {
		t.Fatalf("since %v", out.ParkedSince)
	}
}

func TestParkedSeriesHandler(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	calls := 0
	a := newParkedSeriesAPI(func(context.Context, uuid.UUID, string, time.Time) ([]parkedRow, error) {
		calls++
		return parkedNight(now), nil
	})
	a.now = func() time.Time { return now }
	uid := uuid.New()
	r := chi.NewRouter()
	r.Get("/vehicles/{vehicleID}/parked-series", func(w http.ResponseWriter, req *http.Request) { a.handle(uid, w, req) })
	get := func(q string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vehicles/rv-1/parked-series"+q, nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, m := get("?tz=America/Chicago")
	if code != 200 || len(m["points"].([]any)) != 11 || m["figures"].(map[string]any)["wake_ups"] != float64(2) {
		t.Fatalf("%d %v", code, m)
	}
	get("?tz=America/Chicago")
	if calls != 1 {
		t.Fatalf("second read must come from the cache, store calls %d", calls)
	}
	if code, _ := get("?tz=Nowhere/Land"); code != 400 {
		t.Fatalf("bad tz %d", code)
	}
}

// The public demo serves the Now card's graph for the sample truck only: through the demo gate and the demo user, the real handler answers
// the prod shape (200) on the demo vehicle and the router's 404 on any other id, and a write is refused.
func TestParkedSeriesOnTheDemo(t *testing.T) {
	now := time.Now()
	api := newParkedSeriesAPI(func(_ context.Context, uid uuid.UUID, id string, at time.Time) ([]parkedRow, error) {
		if id != demoVeh {
			t.Errorf("store asked for %q, want only the demo vehicle", id)
		}
		return parkedNight(at), nil
	})
	api.now = func() time.Time { return now }
	r := chi.NewRouter()
	r.Use(demoOnlyGateMW(demoVeh))
	r.Use(demoAccessMW(nil, uuid.New(), demoVeh, newIPLimiter(1000, time.Minute, time.Hour), newIPLimiter(1000, time.Minute, time.Hour)))
	r.Get("/api/vehicles/{vehicleID}/parked-series", withUser(api.handle))

	rec := demoReq(r, "GET", "/api/vehicles/"+demoVeh+"/parked-series?tz=America/Chicago&junk=1")
	if rec.Code != 200 {
		t.Fatalf("demo vehicle = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "from", "to", "parked_since", "parked", "change_pct", "points", "sleeps", "wakes", "figures", "projection"} {
		if _, ok := got[k]; !ok {
			t.Errorf("answer lacks %q: the shape is prod's", k)
		}
	}
	if rec.Header().Get(demoHeader) != "1" {
		t.Errorf("not served as the demo user: header %q", rec.Header().Get(demoHeader))
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/vehicles/other/parked-series"},
		{"POST", "/api/vehicles/" + demoVeh + "/parked-series"},
	} {
		if rec := demoReq(r, c.method, c.path); rec.Code != 404 {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}
