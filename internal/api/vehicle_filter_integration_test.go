//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

// ?vehicle_id=<rivian vehicle id> narrows /api/drives and /api/charges to one of the
// caller's trucks (the Now card's "last drive/charge" with two trucks); without it
// every truck is listed as before; another user's vehicle id, or an unknown one,
// matches nothing.
func TestDrivesAndChargesVehicleFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "vf-a")
	b, _ := db.EnsureUser(ctx, pool, "vf-b")
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)
	now := time.Now().UTC().Truncate(time.Second)
	seed := func(uid uuid.UUID, id, vid string, ago time.Duration) {
		t.Helper()
		st := now.Add(-ago)
		if err := df.For(uid).Upsert(ctx, drives.Drive{ID: "d-" + id, VehicleID: vid, StartedAt: st, EndedAt: st.Add(30 * time.Minute),
			StartSoCPct: 80, EndSoCPct: 70, DistanceMi: 25, EnergyUsedKWh: 8, Source: "live"}); err != nil {
			t.Fatal(err)
		}
		if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: "c-" + id, VehicleID: vid, StartedAt: st, EndedAt: st.Add(30 * time.Minute),
			EnergyAddedKWh: 40, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	seed(a, "a1-old", "vf-rv-1", 5*time.Hour)
	seed(a, "a1-new", "vf-rv-1", 3*time.Hour)
	seed(a, "a2", "vf-rv-2", 1*time.Hour) // the newest overall, on the other truck
	seed(b, "b1", "vf-rv-b", 2*time.Hour)

	ids := func(path string, uid uuid.UUID, charges bool) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		if charges {
			handleCharges(cf.For(uid), sf.For(uid), nil)(w, req)
		} else {
			handleDrives(df.For(uid), cf.For(uid), sf.For(uid))(w, req)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	for _, charges := range []bool{false, true} {
		p := "d-"
		base := "/api/drives"
		if charges {
			p, base = "c-", "/api/charges"
		}
		if got := ids(base+"?limit=1", a, charges); len(got) != 1 || got[0] != p+"a2" {
			t.Errorf("%s without a filter: %v, want the newest of every truck", base, got)
		}
		if got := ids(base+"?limit=1&vehicle_id=vf-rv-1", a, charges); len(got) != 1 || got[0] != p+"a1-new" {
			t.Errorf("%s truck 1: %v, want its own latest", base, got)
		}
		if got := ids(base+"?vehicle_id=vf-rv-2", a, charges); len(got) != 1 || got[0] != p+"a2" {
			t.Errorf("%s truck 2: %v", base, got)
		}
		if got := ids(base+"?vehicle_id=vf-rv-b", a, charges); len(got) != 0 {
			t.Errorf("%s another user's truck: %v, want none", base, got)
		}
		if got := ids(base+"?vehicle_id=nope", a, charges); len(got) != 0 {
			t.Errorf("%s unknown truck: %v, want none", base, got)
		}
	}
}
