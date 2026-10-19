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

func TestColdRangeFromOwnDriveWeather(t *testing.T) {
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
	alice, bob := mkUser("cold-alice"), mkUser("cold-bob")
	mkVeh := func(uid uuid.UUID, rid, model string, pack float64) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, trim, model_year, pack_kwh) VALUES ($1,$2,$3,'PKG-ADV',2023,$4) RETURNING id`, uid, rid, model, pack).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	av, bv, r2 := mkVeh(alice, "rv-cold-a", "R1T", 100), mkVeh(bob, "rv-cold-b", "R1S", 100), mkVeh(alice, "rv-cold-r2", "R2", 90)
	at := time.Now().Add(-48 * time.Hour)
	drive := func(vid, uid uuid.UUID, id string, mi, kwh float64, tempC *float64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, energy_used_kwh, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'live')`, uid, vid, id, at, at.Add(time.Hour), mi, kwh); err != nil {
			t.Fatal(err)
		}
		if tempC != nil {
			if _, err := pool.ExecContext(ctx, `INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c)
				VALUES ($1,$2,30.3,-97.7,$3,'open-meteo',$4)`, uid, id, at.Truncate(time.Hour), *tempC); err != nil {
				t.Fatal(err)
			}
		}
	}
	f := func(v float64) *float64 { return &v }
	// Six cold drives at 2.0 mi/kWh and -7 °C: range at 100 kWh is 200 mi.
	for i := 0; i < 6; i++ {
		drive(av, alice, "cold-"+string(rune('a'+i)), 20, 10, f(-7))
	}
	drive(av, alice, "cold-noweather", 20, 10, nil) // no stored temperature: not counted
	drive(av, alice, "cold-mild-1", 20, 5, f(15))   // only one mild drive
	drive(bv, bob, "cold-bob", 200, 50, f(-7))      // someone else's
	drive(r2, alice, "cold-r2", 30, 10, f(-7))      // an R2
	drive(av, alice, "cold-short", 2, 0.5, f(-7))   // below the drive filter
	drive(av, alice, "cold-glitch", 20, 1.5, f(-7)) // 13 mi/kWh: not plausible

	serve := func(uid uuid.UUID, rid, query string) coldRange {
		t.Helper()
		r := chi.NewRouter()
		r.Get("/vehicles/{vehicleID}/cold-range", func(w http.ResponseWriter, req *http.Request) { handleColdRange(pool)(uid, w, req) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/vehicles/"+rid+"/cold-range"+query, nil))
		if w.Code != 200 {
			t.Fatalf("code %d: %s", w.Code, w.Body.String())
		}
		var v coldRange
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	got := serve(alice, "rv-cold-a", "?temp_c=-6.7&grid=-17.8,-6.7,15")
	if got.Reason != "" || got.EfficiencyLow == nil || *got.EfficiencyLow != 2 || *got.EfficiencyHigh != 2 {
		t.Fatalf("cold = %+v", got)
	}
	if got.RangeLow == nil || *got.RangeLow != 200 || *got.RangeHigh != 200 {
		t.Errorf("range = %v..%v, want 200 (2.0 mi/kWh x the truck's 100 kWh)", got.RangeLow, got.RangeHigh)
	}
	if got.BasedOn == nil || got.BasedOn.Drives != 6 || got.BasedOn.Miles != 120 {
		t.Errorf("based_on = %+v, want the six drives with a stored temperature", got.BasedOn)
	}
	if len(got.Available) != 1 || got.Available[0] != -6.7 {
		t.Errorf("available = %v, want only -6.7 (no history at -17.8, one mild drive at 15)", got.Available)
	}
	if got.MinDrives != 5 || got.MinMiles != 50 || got.DistanceUnit != "mi" {
		t.Errorf("thresholds = %+v", got)
	}

	// Where there is no history the answer says so and still carries the rule.
	none := serve(alice, "rv-cold-a", "?temp_c=-17.8")
	if none.Reason != "not_enough_data" || none.EfficiencyLow != nil || none.RangeLow != nil || none.BasedOn != nil || none.MinDrives != 5 {
		t.Errorf("no history = %+v", none)
	}
	// Metric units change the distance and efficiency, not the rule.
	m := serve(alice, "rv-cold-a", "?temp_c=-6.7&units=metric")
	if m.DistanceUnit != "km" || m.MinMiles != 80 || m.RangeLow == nil || *m.RangeLow != 322 {
		t.Errorf("metric = %+v (range %v)", m, m.RangeLow)
	}
	// An R2 is not tracked.
	if r := serve(alice, "rv-cold-r2", "?temp_c=-6.7"); r.Reason != "not_tracked" || r.EfficiencyLow != nil {
		t.Errorf("r2 = %+v", r)
	}
	// Another user's truck is not found.
	rt := chi.NewRouter()
	rt.Get("/vehicles/{vehicleID}/cold-range", func(w http.ResponseWriter, req *http.Request) { handleColdRange(pool)(bob, w, req) })
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("GET", "/vehicles/rv-cold-a/cold-range?temp_c=0", nil))
	if w.Code != 404 {
		t.Errorf("other user's truck: code %d, want 404", w.Code)
	}
	// Bad input.
	w = httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("GET", "/vehicles/rv-cold-b/cold-range", nil))
	if w.Code != 400 {
		t.Errorf("missing temp_c: code %d, want 400", w.Code)
	}
}
