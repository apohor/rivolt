package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestBuildDrain_WindowsSufficiencyAndDaily(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC) // 10:00 local
	pack := 135.0
	raw := drainRaw{
		PackKWh: &pack,
		Days: map[string]drainAgg{
			"2026-10-14": {ParkH: 13, DropPct: 0.5, ObsH: 14, AwakeH: 0.5, ChargingH: 1, Sleeps: 2, Quick: 1},
			"2026-10-13": {ParkH: 20, DropPct: 0.9, ObsH: 22, AwakeH: 1.0, Sleeps: 3},
			"2026-10-12": {ParkH: 1, DropPct: 0.05}, // under 12 h: excluded
			"2026-10-11": {ParkH: 8, DropPct: 2.0},   // 3-12 h: still excluded from the bar
		},
		W7:  drainAgg{ParkH: 26, DropPct: 1.15, ObsH: 28, AwakeH: 1.5, Sleeps: 5, Quick: 1},
		W30: drainAgg{ParkH: 26, DropPct: 1.15, ObsH: 28, AwakeH: 1.5, Sleeps: 5, Quick: 1}, // under 72 h
	}
	got := buildDrain(raw, now, loc)
	if len(got.Daily) != 30 || got.Daily[29].Day != "2026-10-14" || got.Daily[0].Day != "2026-09-15" {
		t.Fatalf("daily = %d rows, first %s last %s", len(got.Daily), got.Daily[0].Day, got.Daily[29].Day)
	}
	w7, w30 := got.Windows["7d"], got.Windows["30d"]
	if !w7.Sufficient || w7.DrainPctPerDay == nil || *w7.DrainPctPerDay != 1.06 { // 24*1.15/26
		t.Fatalf("7d = %+v", w7)
	}
	if w7.DrainKWhPerDay == nil || *w7.DrainKWhPerDay != 1.43 { // 1.06% of 135 kWh
		t.Fatalf("kwh/day = %v", w7.DrainKWhPerDay)
	}
	if w30.Sufficient || w30.DrainPctPerDay != nil || w30.DrainKWhPerDay != nil {
		t.Fatalf("30d must be insufficient with null rate: %+v", w30)
	}
	if w7.DaysInWindow != 7 || w7.DaysWithData != 4 || w7.DaysExcluded["insufficient"] != 5 {
		t.Fatalf("7d days: %+v", w7)
	}
	today, d12, quiet := got.Daily[29], got.Daily[27], got.Daily[10]
	if today.DrainPct == nil || *today.DrainPct != 0.92 || today.Excluded != nil || today.ChargingHours != 1 {
		t.Fatalf("today = %+v", today)
	}
	if d11 := got.Daily[26]; d11.DrainPct != nil || d11.Excluded == nil {
		t.Fatalf("8 h parked day must be insufficient (12 h floor): %+v", d11)
	}
	if d12.DrainPct != nil || d12.Excluded == nil || *d12.Excluded != "insufficient" {
		t.Fatalf("10-12 should be insufficient: %+v", d12)
	}
	if quiet.DrainPct != nil || quiet.IdleAwakeHours != nil || quiet.Excluded == nil {
		t.Fatalf("no-data day must be null, never 0: %+v", quiet)
	}
}

func TestBuildDrain_UnknownPackAndNoData(t *testing.T) {
	got := buildDrain(drainRaw{Days: map[string]drainAgg{}}, time.Now(), time.UTC)
	w := got.Windows["7d"]
	if got.PackKWh != nil || w.Sufficient || w.DrainPctPerDay != nil || w.IdleAwakeHoursPerDay != nil || w.MedianSleepSeconds != nil {
		t.Fatalf("empty data must be all null: %+v", w)
	}
	if w.DaysWithData != 0 || len(got.Daily) != 30 {
		t.Fatalf("%+v", w)
	}
}

func TestDrainHandler_TZValidationAndCache(t *testing.T) {
	calls := 0
	a := newDrainAPI(func(context.Context, uuid.UUID, string, *time.Location, time.Time) (drainRaw, error) {
		calls++
		return drainRaw{Days: map[string]drainAgg{}}, nil
	})
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	r := chi.NewRouter()
	uid := uuid.New()
	r.Get("/vehicles/{vehicleID}/drain", func(w http.ResponseWriter, req *http.Request) { a.handle(uid, w, req) })
	get := func(path string) int {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if get("/vehicles/rv-1/drain?tz=Not/AZone") != http.StatusBadRequest {
		t.Fatal("bad tz must 400")
	}
	if get("/vehicles/rv-1/drain?tz=America/Chicago") != 200 || get("/vehicles/rv-1/drain?tz=America/Chicago") != 200 {
		t.Fatal("200 expected")
	}
	if calls != 1 {
		t.Fatalf("second call within TTL should hit the cache, store calls = %d", calls)
	}
	a.now = func() time.Time { return t0.Add(drainCacheTTL + time.Second) }
	get("/vehicles/rv-1/drain?tz=America/Chicago")
	if calls != 2 {
		t.Fatalf("expired entry must reload, calls = %d", calls)
	}
	get("/vehicles/rv-2/drain?tz=America/Chicago")
	if calls != 3 {
		t.Fatalf("cache must be per vehicle, calls = %d", calls)
	}
}
