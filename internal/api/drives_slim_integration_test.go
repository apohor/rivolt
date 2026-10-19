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

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

func TestDrivesSlimListAndGetOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "slim-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "slim-bob")
	if err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)

	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	if err := df.For(alice).Upsert(ctx, drives.Drive{ID: "live_slim_d_1", VehicleID: "rv-slim-a", StartedAt: start, EndedAt: start.Add(40 * time.Minute),
		StartSoCPct: 80, EndSoCPct: 70, DistanceMi: 25, EnergyUsedKWh: 8, Source: "live", RoutePolyline: "_p~iF~ps|U_ulLnnqC_mqNvxq`@"}); err != nil {
		t.Fatal(err)
	}

	serve := func(uidIsAlice bool, path string) *httptest.ResponseRecorder {
		uid := alice
		if !uidIsAlice {
			uid = bob
		}
		r := chi.NewRouter()
		r.Get("/api/drives", handleDrives(df.For(uid), cf.For(uid), sf.For(uid)))
		r.Get("/api/drives/{id}", handleDriveGet(df.For(uid), cf.For(uid), sf.For(uid)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	poly := func(w *httptest.ResponseRecorder) (n int, polyline string) {
		var rows []struct{ RoutePolyline string }
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("list body: %v %s", err, w.Body.String())
		}
		if len(rows) > 0 {
			polyline = rows[0].RoutePolyline
		}
		return len(rows), polyline
	}

	if n, p := poly(serve(true, "/api/drives?limit=10")); n != 1 || p == "" {
		t.Fatalf("full list: %d rows, polyline %q", n, p)
	}
	if n, p := poly(serve(true, "/api/drives?limit=10&slim=1")); n != 1 || p != "" {
		t.Fatalf("slim list: %d rows, polyline %q (must be empty)", n, p)
	}
	w := serve(true, "/api/drives/live_slim_d_1")
	var one struct {
		ID            string
		RoutePolyline string
		DistanceMi    float64
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &one) != nil || one.ID != "live_slim_d_1" || one.RoutePolyline == "" || one.DistanceMi != 25 {
		t.Fatalf("get one: %d %s", w.Code, w.Body.String())
	}
	if w := serve(true, "/api/drives/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", w.Code)
	}
	if w := serve(false, "/api/drives/live_slim_d_1"); w.Code != http.StatusNotFound {
		t.Errorf("another user's drive = %d, want 404", w.Code)
	}
}
