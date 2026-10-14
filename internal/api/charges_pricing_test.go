package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/ledger"
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
	if got := decorateCharge(free, cfg, nil); got.EstimatedCost != 0 {
		t.Errorf("free session estimated at %v, want none", got.EstimatedCost)
	}
	unpriced := charges.Charge{EnergyAddedKWh: 20, MaxPowerKW: 1.4}
	if got := decorateCharge(unpriced, cfg, nil); got.EstimatedCost != 20*0.11 {
		t.Errorf("unpriced AC estimate = %v, want %v", got.EstimatedCost, 20*0.11)
	}
	home := func() (float64, float64, bool) { return 30.27, -97.74, true }
	away := charges.Charge{EnergyAddedKWh: 20, MaxPowerKW: 1.4, Lat: 35.47, Lon: -97.52}
	if got := decorateCharge(away, cfg, home); got.EstimatedCost != 0 {
		t.Errorf("away AC estimated at %v, want none (unknown until priced)", got.EstimatedCost)
	}
	atHome := charges.Charge{EnergyAddedKWh: 20, MaxPowerKW: 7.6, Lat: 30.2702, Lon: -97.7401}
	if got := decorateCharge(atHome, cfg, home); got.EstimatedCost != 20*0.11 {
		t.Errorf("home AC estimate = %v, want %v", got.EstimatedCost, 20*0.11)
	}
}

// A10: /api/charges rows carry the ledger's classification next to the
// stored row (cost_source_effective, kind, place, ledger_flags, ...).
func TestChargeResponse_CarriesClassification(t *testing.T) {
	resp := chargeResponse{
		Charge:         charges.Charge{ID: "c1", CostSource: "home_rate", Place: "home"},
		Classification: &ledger.Classification{CostSource: "unknown", Kind: "ac", Place: "away", Flags: []string{"unpriced"}},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"CostSource":"home_rate"`, `"Place":"home"`, `"cost_source_effective":"unknown"`,
		`"kind":"ac"`, `"place":"away"`, `"ledger_flags":["unpriced"]`, `"counts_in_spend":false`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s: %s", want, s)
		}
	}
}

// F5: without the user's charging settings the classification would be
// wrong, so it's omitted and flagged rather than guessed.
func TestChargeResponse_ClassificationUnavailable(t *testing.T) {
	raw, err := json.Marshal(chargeResponse{Charge: charges.Charge{ID: "c1"}, ClassificationUnavailable: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"classification_unavailable":true`) || strings.Contains(s, "cost_source_effective") ||
		strings.Contains(s, `"kind"`) {
		t.Errorf("unavailable classification JSON = %s", s)
	}
}
