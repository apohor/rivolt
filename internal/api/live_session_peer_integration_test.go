//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// liveSnapStore is a LiveStateStore holding the owner pod's published accumulator.
type liveSnapStore struct {
	mu   sync.Mutex
	snap rivian.LiveStateSnapshot
	ok   bool
}

func (s *liveSnapStore) Save(_ context.Context, _ string, snap rivian.LiveStateSnapshot, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap, s.ok = snap, true
	return nil
}
func (s *liveSnapStore) Load(context.Context, string) (rivian.LiveStateSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap, s.ok, nil
}
func (s *liveSnapStore) Delete(context.Context, string) error { return nil }
func (s *liveSnapStore) PutState(context.Context, string, *rivian.State, time.Duration) error {
	return nil
}
func (s *liveSnapStore) GetState(context.Context, string) (*rivian.State, error) { return nil, nil }

// GET /api/live-session/{id} on a pod that does not hold the vehicle's lease answers from the owner's published accumulator,
// so the estimated cost is the same on every pod; a snapshot that has gone stale gives the zeroed payload.
func TestLiveSessionOnAPeerPodMatchesTheOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "live-peer")
	if err != nil {
		t.Fatal(err)
	}
	store, err := settings.OpenStore(pool, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.SetChargingConfig(ctx, store, settings.ChargingConfig{HomePricePerKWh: 0.20, HomeCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}

	const vid = "veh-peer"
	now := time.Now()
	shared := &liveSnapStore{}
	_ = shared.Save(ctx, vid, rivian.LiveStateSnapshot{ChargeCounter: 1, Charge: &rivian.LiveChargeSnapshot{
		ID: "live_" + vid + "_c_1", Number: 1, StartedAt: now.Add(-2 * time.Hour), StartSoC: 30, EndAt: now, EndSoC: 45,
		MaxPower: 7.5, FinalState: "charging_active", LastMeaningfulAt: now, EnergyIntKWh: 6, ActiveSeconds: 7200,
	}}, time.Hour)
	peer := rivian.NewStateMonitor(nil, nil)
	peer.SetLiveStateStore(shared)

	h := handleLiveSession(&rivian.LiveClient{}, peer, store)
	get := func() map[string]any {
		r := chi.NewRouteContext()
		r.URLParams.Add("vehicleID", vid)
		req := httptest.NewRequest(http.MethodGet, "/api/live-session/"+vid, nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, r))
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	out := get()
	if cost, _ := out["estimated_cost"].(float64); cost < 1.19 || cost > 1.21 { // 6 kWh at $0.20
		t.Errorf("peer estimated_cost = %v, want about 1.20 (%v)", out["estimated_cost"], out)
	}
	if out["estimated_currency"] != "USD" {
		t.Errorf("currency = %v", out["estimated_currency"])
	}

	// A snapshot older than the stale limit: the zeroed payload, no estimate.
	stale := shared.snap
	c := *stale.Charge
	c.EndAt = now.Add(-3 * time.Hour)
	stale.Charge = &c
	_ = shared.Save(ctx, vid, stale, time.Hour)
	fresh := rivian.NewStateMonitor(nil, nil) // a new viewer pod: no read cache
	fresh.SetLiveStateStore(shared)
	h = handleLiveSession(&rivian.LiveClient{}, fresh, store)
	out = get()
	if _, has := out["estimated_cost"]; has {
		t.Errorf("a stale snapshot priced a charge: %v", out)
	}
	if out["vehicle_id"] != vid {
		t.Errorf("zeroed payload lost the vehicle id: %v", out)
	}
}
