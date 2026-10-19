//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
)

// A drive or charge whose month of raw telemetry has been thinned says
// samples_reduced; a month never thinned, or thinned but held at full detail by an
// admin restore (rethin_after in the future), says nothing; a session spanning a
// boundary counts when either month is reduced. /api/samples names the reduced
// months of the range in X-Rivolt-Reduced-Months for the vehicle asked about.
func TestSamplesReducedSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "reduced-signal")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "reduced-other")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-reduced','R1S') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 2031 keeps these months clear of every other test's thinning rows.
	// 2031-03 thinned; 2031-04 thinned but held at full detail by a restore; 2031-05 never thinned.
	for _, m := range []string{"2031-03-01", "2031-04-01"} {
		exec(`INSERT INTO sample_tiers (month, rows_before, rows_after, bytes_before, bytes_after) VALUES ($1::date, 100, 10, 1000, 100) ON CONFLICT (month) DO NOTHING`, m)
	}
	exec(`INSERT INTO archive_months (user_id, vehicle_id, month, rows, first_at, last_at, sha256_object, bytes, rethin_after)
		VALUES ($1,$2,'2031-04-01',100,'2031-04-01','2031-04-30','x',1, now() + interval '3 days')`, uid, vid)
	session := func(table, id, start, end string) {
		if table == "drives" {
			exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,$3,$4::timestamptz,$5::timestamptz,5,'live')`, uid, vid, id, start, end)
			return
		}
		exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source) VALUES ($1,$2,$3,$4::timestamptz,$5::timestamptz,20,'live')`, uid, vid, id, start, end)
	}
	session("drives", "d-march", "2031-03-10T12:00:00Z", "2031-03-10T12:30:00Z")
	session("drives", "d-april-held", "2031-04-10T12:00:00Z", "2031-04-10T12:30:00Z")
	session("drives", "d-may", "2031-05-10T12:00:00Z", "2031-05-10T12:30:00Z")
	session("drives", "d-cross", "2031-03-31T23:50:00Z", "2031-04-01T00:20:00Z")      // March is reduced
	session("drives", "d-cross-held", "2031-04-30T23:50:00Z", "2031-05-01T00:20:00Z") // April held, May never thinned
	session("charges", "c-march", "2031-03-12T12:00:00Z", "2031-03-12T13:00:00Z")
	session("charges", "c-may", "2031-05-12T12:00:00Z", "2031-05-12T13:00:00Z")

	rf := db.NewVehicleResolverFactory(pool)
	df, cf, sf, ss := drives.NewFactory(pool, rf), charges.NewFactory(pool, rf), settings.NewFactory(pool), samples.NewFactory(pool, rf)
	reduced := reducedFrom(ss.For(uid))
	r := chi.NewRouter()
	r.Get("/api/drives/{id}", handleDriveGet(df.For(uid), cf.For(uid), sf.For(uid), reduced))
	r.Get("/api/charges/{id}", handleGetCharge(cf.For(uid), sf.For(uid), reduced))
	r.Get("/api/samples", handleSamples(ss.For(uid), reduced))
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil).WithContext(auth.WithUser(ctx, uid)))
		if w.Code != 200 {
			t.Fatalf("%s = %d: %s", path, w.Code, w.Body.String())
		}
		return w
	}
	for path, want := range map[string]bool{
		"/api/drives/d-march": true, "/api/drives/d-april-held": false, "/api/drives/d-may": false,
		"/api/drives/d-cross": true, "/api/drives/d-cross-held": false,
		"/api/charges/c-march": true, "/api/charges/c-may": false,
	} {
		w := get(path)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		v, has := body["samples_reduced"]
		if has != want || (has && v != true) {
			t.Errorf("%s samples_reduced = %v (present %v), want present=%v", path, v, has, want)
		}
	}

	// /api/samples: the reduced months of the range for the named vehicle.
	w := get("/api/samples?vehicle=rv-reduced&since=2031-03-01T00:00:00Z&until=2031-05-31T00:00:00Z")
	if h := w.Header().Get("X-Rivolt-Reduced-Months"); h != "2031-03" {
		t.Errorf("reduced months = %q, want only 2031-03 (April is held at full detail, May was never thinned)", h)
	}
	if h := get("/api/samples?vehicle=rv-reduced&since=2031-05-01T00:00:00Z&until=2031-05-31T00:00:00Z").Header().Get("X-Rivolt-Reduced-Months"); h != "" {
		t.Errorf("an unthinned range has header %q, want none", h)
	}
	if h := get("/api/samples?since=2031-03-01T00:00:00Z&until=2031-05-31T00:00:00Z").Header().Get("X-Rivolt-Reduced-Months"); h != "" {
		t.Errorf("no vehicle named, header %q, want none", h)
	}
	// Another user asking about this vehicle id learns nothing.
	months, err := ss.For(other).ReducedMonths(ctx, "rv-reduced", time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2031, 5, 31, 0, 0, 0, 0, time.UTC))
	if err != nil || len(months) != 0 {
		t.Errorf("other user's lookup = %v (%v), want none", months, err)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "json") {
		t.Errorf("samples body is not JSON: %q", w.Header().Get("Content-Type"))
	}
}
