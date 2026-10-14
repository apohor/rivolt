package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/settings"
)

type fakePricer struct {
	calls []string
	found bool
}

func (f *fakePricer) UpdatePricing(_ context.Context, id string, cost float64, cur string, ppk float64) (int64, error) {
	f.calls = append(f.calls, "update")
	if !f.found {
		return 0, nil
	}
	return 1, nil
}

func (f *fakePricer) MarkFree(_ context.Context, id, cur string) (int64, error) {
	f.calls = append(f.calls, "free:"+id+":"+cur)
	if !f.found {
		return 0, nil
	}
	return 1, nil
}

func patchPricing(t *testing.T, p chargePricer, body string) int {
	t.Helper()
	r := chi.NewRouter()
	r.Patch("/charges/{id}/pricing", patchChargePricing(p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/charges/c-1/pricing", strings.NewReader(body)))
	return rec.Code
}

func TestPatchChargePricing_Free(t *testing.T) {
	f := &fakePricer{found: true}
	if code := patchPricing(t, f, `{"free": true, "currency": "usd"}`); code != http.StatusNoContent {
		t.Fatalf("free = %d, want 204", code)
	}
	if strings.Join(f.calls, ",") != "free:c-1:USD" {
		t.Errorf("calls = %v, want MarkFree(c-1, USD)", f.calls)
	}

	// Zero still means "clear", not free.
	f = &fakePricer{found: true}
	if code := patchPricing(t, f, `{"cost": 0}`); code != http.StatusNoContent || strings.Join(f.calls, ",") != "update" {
		t.Errorf("cost 0 = %d %v, want 204 via UpdatePricing", code, f.calls)
	}
	f = &fakePricer{found: true}
	if code := patchPricing(t, f, `{"free": false, "cost": 4.2}`); code != http.StatusNoContent || strings.Join(f.calls, ",") != "update" {
		t.Errorf("free:false = %d %v, want UpdatePricing", code, f.calls)
	}

	f = &fakePricer{found: true}
	if code := patchPricing(t, f, `{"free": true, "cost": 3}`); code != http.StatusBadRequest || len(f.calls) != 0 {
		t.Errorf("free+cost = %d %v, want 400 and no write", code, f.calls)
	}
	if code := patchPricing(t, &fakePricer{found: false}, `{"free": true}`); code != http.StatusNotFound {
		t.Errorf("unknown charge = %d, want 404", code)
	}
	if code := handlePatchChargePricingStatus(t); code != http.StatusServiceUnavailable {
		t.Errorf("nil store = %d, want 503", code)
	}
}

func handlePatchChargePricingStatus(t *testing.T) int {
	t.Helper()
	rec := httptest.NewRecorder()
	handlePatchChargePricing(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{}`)))
	return rec.Code
}

func TestDecorateCharge_FreeGetsNoEstimate(t *testing.T) {
	cfg := settings.ChargingConfig{HomePricePerKWh: 0.11, HomeCurrency: "USD"}
	free := charges.Charge{EnergyAddedKWh: 20, MaxPowerKW: 1.4, Cost: 0, CostSource: "user"}
	if got := decorateCharge(free, cfg); got.EstimatedCost != 0 {
		t.Errorf("free session estimated at %v, want none", got.EstimatedCost)
	}
	unpriced := charges.Charge{EnergyAddedKWh: 20, MaxPowerKW: 1.4}
	if got := decorateCharge(unpriced, cfg); got.EstimatedCost != 20*0.11 {
		t.Errorf("unpriced AC estimate = %v, want %v", got.EstimatedCost, 20*0.11)
	}
}
