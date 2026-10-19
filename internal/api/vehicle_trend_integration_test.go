//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

func TestVehicleTrendsForOneTruck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mkUser := func(name string) uuid.UUID {
		u, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	alice, bob := mkUser("trends-alice"), mkUser("trends-bob")
	mkVeh := func(uid uuid.UUID, rid, model string) uuid.UUID {
		var id uuid.UUID
		// The trim names no pack (like the real PKG-*/BLD-* ids), so the spec is unknown.
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, trim, model_year, pack_kwh) VALUES ($1,$2,$3,'PKG-ADV',2023,123.4) RETURNING id`, uid, rid, model).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	av, bv, r2 := mkVeh(alice, "rv-trends-a", "R1T"), mkVeh(bob, "rv-trends-b", "R1S"), mkVeh(alice, "rv-trends-r2", "R2")
	// Alice reads her months in Los Angeles time.
	if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, timezone) VALUES ($1,'America/Los_Angeles') ON CONFLICT (user_id) DO UPDATE SET timezone = EXCLUDED.timezone`, alice); err != nil {
		t.Fatalf("engagement_prefs: %v", err)
	}

	la, _ := time.LoadLocation("America/Los_Angeles")
	now := time.Now().In(la)
	monthStart := func(back int) time.Time {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, la).AddDate(0, -back, 0)
	}
	drive := func(vid, uid uuid.UUID, id string, at time.Time, mi, kwh float64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, energy_used_kwh, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'live')`, uid, vid, id, at, at.Add(40*time.Minute), mi, kwh); err != nil {
			t.Fatal(err)
		}
	}
	// Month -2: 60 mi at 3.0 mi/kWh in three drives, plus the ones the filters drop.
	for i := 0; i < 3; i++ {
		drive(av, alice, "tr-2-"+string(rune('a'+i)), monthStart(2).Add(time.Duration(10+i)*time.Hour), 20, 20.0/3)
	}
	drive(av, alice, "tr-2-short", monthStart(2).Add(30*time.Hour), 2, 0.5)
	drive(av, alice, "tr-2-noenergy", monthStart(2).Add(31*time.Hour), 40, 0)
	// A drive at 00:30 local on the 1st of month -1 (07:30 UTC) belongs to month -1 in LA time.
	drive(av, alice, "tr-1-edge", monthStart(1).Add(30*time.Minute), 60, 15)
	drive(bv, bob, "tr-b", monthStart(1).Add(48*time.Hour), 100, 10)
	drive(av, alice, "tr-old", monthStart(30), 30, 10)

	// Capacity: six clean samples over 40 days, one derated.
	for i, c := range []struct {
		kwh    float64
		derate bool
		days   int
	}{{136, false, 50}, {138, false, 40}, {137, false, 30}, {139, false, 20}, {140, false, 10}, {99, true, 5}, {138, false, 1}} {
		at := now.Add(-time.Duration(c.days) * 24 * time.Hour)
		var cid uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source)
			VALUES ($1,$2,$3,$4,$5,50,'live') RETURNING id`, alice, av, "trc-"+string(rune('a'+i)), at, at.Add(time.Hour)).Scan(&cid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_pack_health_samples (vehicle_id, charge_id, at, pack_kwh_effective, soc_delta_pct, energy_delivered_kwh, derate_active)
			VALUES ($1,$2,$3,$4,50,60,$5)`, av, cid, at, c.kwh, c.derate); err != nil {
			t.Fatal(err)
		}
	}
	drive(r2, alice, "tr-r2", monthStart(1).Add(48*time.Hour), 50, 15)

	serve := func(uid uuid.UUID, rid, query string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Get("/vehicles/{vehicleID}/trends", func(w http.ResponseWriter, req *http.Request) { handleVehicleTrends(pool)(uid, w, req) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/vehicles/"+rid+"/trends"+query, nil))
		return w
	}
	decode := func(w *httptest.ResponseRecorder) vehicleTrends {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("code %d: %s", w.Code, w.Body.String())
		}
		var v vehicleTrends
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	got := decode(serve(alice, "rv-trends-a", ""))
	if got.Units != "imperial" || got.DistanceUnit != "mi" || got.EfficiencyUnit != "mi/kWh" || got.Months != 12 || got.MinMonthDistance != 50 || got.Timezone != "America/Los_Angeles" {
		t.Errorf("meta = %+v", got)
	}
	if len(got.Efficiency) != 2 || got.Efficiency[0].Month != monthStart(2).Format("2006-01") || got.Efficiency[1].Month != monthStart(1).Format("2006-01") {
		t.Fatalf("efficiency = %+v, want months -2 and -1 (the edge drive in LA time, no short/zero-energy/other-user/old drive)", got.Efficiency)
	}
	if e := got.Efficiency[0]; e.Drives != 3 || e.Distance != 60 || e.Efficiency != 3 {
		t.Errorf("month -2 = %+v, want 3 drives, 60 mi, 3.0 mi/kWh", e)
	}
	if e := got.Efficiency[1]; e.Drives != 1 || e.Distance != 60 || e.Efficiency != 4 {
		t.Errorf("month -1 = %+v, want the 00:30 local drive: 60 mi at 4.0", e)
	}
	if got.QualifyingMonths != 2 {
		t.Errorf("qualifying months = %d, want 2 (both have 50+ mi)", got.QualifyingMonths)
	}
	c := got.Capacity
	if c == nil || c.QualifyingCnt != 6 || c.MedianKWh == nil || *c.MedianKWh != 138 || c.SpanDays < 30 || c.MinSamples != 5 || c.MinSpanDays != 30 || c.MinSoCDeltaPct != 30 || c.MinPeakKW != 20 ||
		c.ReportedKWh == nil || *c.ReportedKWh != 123.4 || c.SpecKWh != nil {
		t.Errorf("capacity = %+v, want 6 clean samples (derated excluded), median 138 over 30+ days", c)
	}

	// A truck whose trim names its pack gets a spec; the reported value stays separate.
	if _, err := pool.ExecContext(ctx, `UPDATE vehicles SET trim = 'MAX-QM' WHERE id = $1`, av); err != nil {
		t.Fatal(err)
	}
	if c2 := decode(serve(alice, "rv-trends-a", "")).Capacity; c2 == nil || c2.SpecKWh == nil || *c2.SpecKWh != 141 || c2.ReportedKWh == nil || *c2.ReportedKWh != 123.4 {
		t.Errorf("identified pack: capacity = %+v, want spec 141 and reported 123.4", c2)
	}

	// Metric: km and km/kWh.
	m := decode(serve(alice, "rv-trends-a", "?units=metric&months=3"))
	if m.DistanceUnit != "km" || m.EfficiencyUnit != "km/kWh" || m.MinMonthDistance != 80 || m.Months != 3 || m.Efficiency[0].Distance < 96 || m.Efficiency[0].Distance > 97 {
		t.Errorf("metric = %+v", m)
	}
	// Bad months.
	if w := serve(alice, "rv-trends-a", "?months=99"); w.Code != http.StatusBadRequest {
		t.Errorf("months=99 = %d, want 400", w.Code)
	}
	// An R2: efficiency yes, capacity null (cannot be tracked there).
	r2t := decode(serve(alice, "rv-trends-r2", ""))
	if r2t.Capacity != nil || len(r2t.Efficiency) != 1 {
		t.Errorf("R2 = capacity %+v, %d efficiency months, want null capacity and one month", r2t.Capacity, len(r2t.Efficiency))
	}
	// Not another user's truck.
	if w := serve(bob, "rv-trends-a", ""); w.Code != http.StatusNotFound {
		t.Errorf("another user's vehicle = %d, want 404", w.Code)
	}
}
