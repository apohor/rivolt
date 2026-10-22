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

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/packhealth"
)

// /pack-health?limit=N answers the newest N samples, oldest first, with the headline
// exactly as without it (it is worked out from every sample); no limit, every sample.
func TestPackHealthLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "ph-limit")
	cf := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	ph := packhealth.NewStore(pool)
	base := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Hour)
	var vid uuid.UUID
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i) * 48 * time.Hour)
		if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: "ph-c" + string(rune('0'+i)), VehicleID: "ph-rv", StartedAt: at, EndedAt: at.Add(time.Hour),
			StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 78, MaxPowerKW: 150, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
		var cid uuid.UUID
		if err := pool.QueryRowContext(ctx, `SELECT id, vehicle_id FROM charges WHERE user_id = $1 AND external_id = $2`, uid, "ph-c"+string(rune('0'+i))).Scan(&cid, &vid); err != nil {
			t.Fatal(err)
		}
		if err := ph.Upsert(ctx, packhealth.Sample{VehicleID: vid, ChargeID: cid, At: at, PackKWhEffective: 120 + float64(i), SoCDeltaPct: 60, EnergyDeliveredKWh: 78}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(query string) (int, packHealthResponse) {
		r := chi.NewRouter()
		r.Get("/api/vehicles/{vehicleID}/pack-health", func(w http.ResponseWriter, r *http.Request) {
			handlePackHealthGet(pool, ph)(uid, w, r)
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/vehicles/ph-rv/pack-health"+query, nil)
		r.ServeHTTP(w, req.WithContext(auth.WithUser(ctx, uid)))
		var out packHealthResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, all := get("")
	if code != 200 || len(all.Samples) != 6 || all.Headline.SampleCount != 6 {
		t.Fatalf("no limit: %d samples=%d headline=%+v", code, len(all.Samples), all.Headline)
	}
	code, two := get("?limit=2")
	if code != 200 || len(two.Samples) != 2 {
		t.Fatalf("limit=2: %d samples=%d", code, len(two.Samples))
	}
	if two.Samples[0].PackKWhEffective != 124 || two.Samples[1].PackKWhEffective != 125 || !two.Samples[0].At.Before(two.Samples[1].At) {
		t.Errorf("limit=2 did not keep the newest two, oldest first: %+v", two.Samples)
	}
	if two.Headline != all.Headline {
		t.Errorf("headline changed with the limit: %+v vs %+v", two.Headline, all.Headline)
	}
	if _, big := get("?limit=50"); len(big.Samples) != 6 {
		t.Errorf("limit above the count: %d samples, want all 6", len(big.Samples))
	}
	if code, _ := get("?limit=0"); code != http.StatusBadRequest {
		t.Errorf("limit=0: %d, want 400", code)
	}
}
