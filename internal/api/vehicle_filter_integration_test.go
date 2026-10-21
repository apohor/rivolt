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

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

// ?vehicle_id= on the drive and charge lists (the Now card's last drive and
// charge for the truck on screen): one truck's rows, newest first; another
// user's truck lists nothing; no filter lists every truck, as before.
func TestListsFilterByVehicle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "vf-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "vf-bob")
	if err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)

	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	drive := func(uid uuid.UUID, id, vid string, at time.Time) {
		t.Helper()
		if err := df.For(uid).Upsert(ctx, drives.Drive{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(30 * time.Minute),
			StartSoCPct: 80, EndSoCPct: 75, DistanceMi: 12, EnergyUsedKWh: 4, Source: "live", RoutePolyline: "_p~iF~ps|U_ulLnnqC"}); err != nil {
			t.Fatalf("drive %s: %v", id, err)
		}
	}
	charge := func(uid uuid.UUID, id, vid string, at time.Time) {
		t.Helper()
		if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(40 * time.Minute),
			EnergyAddedKWh: 30, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatalf("charge %s: %v", id, err)
		}
	}
	// Alice: truck A's newest drive is older than truck B's.
	drive(alice, "vf-d-a1", "rv-vf-a", base)
	drive(alice, "vf-d-a2", "rv-vf-a", base.Add(1*time.Hour))
	drive(alice, "vf-d-b1", "rv-vf-b", base.Add(2*time.Hour))
	charge(alice, "vf-c-a1", "rv-vf-a", base.Add(30*time.Minute))
	charge(alice, "vf-c-b1", "rv-vf-b", base.Add(90*time.Minute))
	// Bob has his own truck.
	drive(bob, "vf-d-c1", "rv-vf-c", base.Add(3*time.Hour))

	serve := func(uidIsAlice bool, path string) *httptest.ResponseRecorder {
		uid := alice
		if !uidIsAlice {
			uid = bob
		}
		r := chi.NewRouter()
		r.Get("/api/drives", handleDrives(df.For(uid), cf.For(uid), sf.For(uid)))
		r.Get("/api/charges", handleCharges(cf.For(uid), sf.For(uid), nil))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	ids := func(w *httptest.ResponseRecorder) []string {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var rows []struct {
			ID            string
			VehicleID     string
			RoutePolyline string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("body: %v %s", err, w.Body.String())
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID+"@"+r.VehicleID)
		}
		return out
	}
	check := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	check(ids(serve(true, "/api/drives?limit=1")), "vf-d-b1@rv-vf-b")
	check(ids(serve(true, "/api/drives?limit=1&vehicle_id=rv-vf-a")), "vf-d-a2@rv-vf-a")
	check(ids(serve(true, "/api/drives?limit=10&vehicle_id=rv-vf-a")), "vf-d-a2@rv-vf-a", "vf-d-a1@rv-vf-a")
	check(ids(serve(true, "/api/drives?limit=1&vehicle_id=rv-vf-b")), "vf-d-b1@rv-vf-b")
	check(ids(serve(true, "/api/charges?limit=1")), "vf-c-b1@rv-vf-b")
	check(ids(serve(true, "/api/charges?limit=1&vehicle_id=rv-vf-a")), "vf-c-a1@rv-vf-a")
	// Another user's truck, or one nobody has: nothing, not an error.
	check(ids(serve(false, "/api/drives?limit=10&vehicle_id=rv-vf-a")))
	check(ids(serve(false, "/api/charges?limit=10&vehicle_id=rv-vf-a")))
	check(ids(serve(true, "/api/drives?limit=10&vehicle_id=rv-nobody")))
	check(ids(serve(false, "/api/drives?limit=10")), "vf-d-c1@rv-vf-c")

	// slim still drops the route with the filter.
	w := serve(true, "/api/drives?limit=1&slim=1&vehicle_id=rv-vf-a")
	var slim []struct{ RoutePolyline string }
	if err := json.Unmarshal(w.Body.Bytes(), &slim); err != nil || len(slim) != 1 || slim[0].RoutePolyline != "" {
		t.Fatalf("slim + vehicle_id: %v %s", err, w.Body.String())
	}
	if w := serve(true, "/api/charges?vehicle_id=%3Cx%3E"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed vehicle_id = %d, want 400", w.Code)
	}
}
