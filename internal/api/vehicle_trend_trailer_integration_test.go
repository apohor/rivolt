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

// A drive with a trailer connected (the user's correction, else the recorder's reading) is left out of the month's
// figure and reported apart; a drive the user corrected to "no trailer" counts again; an unknown one counts.
func TestTrendsLeaveOutTrailerDrivesAndReportThemApart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "trends-trailer")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, trim, model_year, pack_kwh) VALUES ($1,'rv-trends-trailer','R1T','PKG-ADV',2023,135) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, timezone) VALUES ($1,'UTC') ON CONFLICT (user_id) DO UPDATE SET timezone = EXCLUDED.timezone`, uid); err != nil {
		t.Fatal(err)
	}
	month := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	n := 0
	drive := func(mi, kwh float64, trailer, override *bool) {
		t.Helper()
		n++
		at := month.Add(time.Duration(n) * 3 * time.Hour)
		if _, err := pool.ExecContext(ctx, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, energy_used_kwh, source, trailer, trailer_override)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'live',$8,$9)`, uid, vid, "tt-"+string(rune('a'+n)), at, at.Add(40*time.Minute), mi, kwh, trailer, override); err != nil {
			t.Fatal(err)
		}
	}
	yes, no := true, false
	drive(30, 10, nil, nil)  // unknown: counts (3.0)
	drive(30, 10, &no, nil)  // no trailer: counts
	drive(30, 10, &yes, &no) // recorded as a trailer, the user says not: counts
	drive(30, 30, &yes, nil) // a trailer: left out, 1.0
	drive(20, 20, &no, &yes) // the user says it was a trailer: left out, 1.0

	r := chi.NewRouter()
	r.Get("/vehicles/{vehicleID}/trends", func(w http.ResponseWriter, req *http.Request) { handleVehicleTrends(pool)(uid, w, req) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/vehicles/rv-trends-trailer/trends", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got vehicleTrends
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Efficiency) != 1 {
		t.Fatalf("efficiency = %+v", got.Efficiency)
	}
	e := got.Efficiency[0]
	if e.Drives != 3 || e.Distance != 90 || e.EnergyKWh != 30 || e.Efficiency != 3 {
		t.Errorf("the month's figure = %+v, want the three drives without a trailer: 90 mi over 30 kWh = 3.0", e)
	}
	if e.Trailer == nil || e.Trailer.Drives != 2 || e.Trailer.Distance != 50 || e.Trailer.EnergyKWh != 50 || e.Trailer.Efficiency != 1 {
		t.Errorf("trailer figure = %+v, want 2 drives, 50 mi over 50 kWh = 1.0", e.Trailer)
	}
	if got.ExcludedTrailerDrives != 2 {
		t.Errorf("excluded = %d, want 2", got.ExcludedTrailerDrives)
	}
	// Metric units scale the trailer figure too.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/vehicles/rv-trends-trailer/trends?units=metric", nil))
	var km vehicleTrends
	_ = json.Unmarshal(w.Body.Bytes(), &km)
	if len(km.Efficiency) != 1 || km.Efficiency[0].Trailer == nil || km.Efficiency[0].Trailer.Distance != 80.5 {
		t.Errorf("metric trailer figure = %+v", km.Efficiency)
	}
}
