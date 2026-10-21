package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/settings"
)

type fakePricer struct {
	calls []string
	found bool
	// finalState is the stored charge's final_state ("" = terminal).
	finalState string
	// source / endedAt default to a live row that just updated.
	source  string
	endedAt time.Time
}

func (f *fakePricer) GetByExternalID(_ context.Context, id string) (*charges.Charge, error) {
	if !f.found {
		return nil, nil
	}
	src, ended := f.source, f.endedAt
	if src == "" {
		src = "live"
	}
	if ended.IsZero() {
		ended = time.Now()
	}
	return &charges.Charge{ID: id, FinalState: f.finalState, Source: src, EndedAt: ended}, nil
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

func (f *fakePricer) SetNetworkPrice(_ context.Context, id, network, cur string, ppk float64) (int64, error) {
	f.calls = append(f.calls, fmt.Sprintf("network:%s:%s:%s:%.2f", id, network, cur, ppk))
	if !f.found {
		return 0, nil
	}
	return 1, nil
}

func patchPricing(t *testing.T, p chargePricer, body string) int {
	t.Helper()
	r := chi.NewRouter()
	r.Patch("/charges/{id}/pricing", patchChargePricing(p, nil))
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
	handlePatchChargePricing(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{}`)))
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

func TestPatchChargePricing_OpenSessionIs409(t *testing.T) {
	for _, st := range []string{"charging_active", "charging_ready", "charging_connecting", "waiting_on_charger"} {
		for _, body := range []string{
			`{"network_slug":"` + dcfcrates.Networks[0].Slug + `"}`,
			`{"cost": 12.5, "price_per_kwh": 0.4}`,
			`{"free": true}`,
		} {
			f := &fakePricer{found: true, finalState: st}
			if code := patchPricing(t, f, body); code != http.StatusConflict {
				t.Errorf("%s %s = %d, want 409", st, body, code)
			}
			if len(f.calls) != 0 {
				t.Errorf("%s %s wrote: %v", st, body, f.calls)
			}
		}
	}
	// Terminal states still price; an unknown charge is 404 before any write.
	for _, st := range []string{"charging_complete", "charging_user_stopped", ""} {
		f := &fakePricer{found: true, finalState: st}
		if code := patchPricing(t, f, `{"cost": 12.5}`); code != http.StatusNoContent {
			t.Errorf("terminal %q = %d, want 204", st, code)
		}
	}
	// A row left in an open state that stopped updating (crash, lost
	// stream) is not in progress: it must stay editable, or it could never
	// be priced. Same for a non-live source.
	for _, f := range []*fakePricer{
		{found: true, finalState: "charging_ready", endedAt: time.Now().Add(-2 * time.Hour)},
		{found: true, finalState: "charging_active", endedAt: time.Now().Add(-31 * time.Minute)},
		{found: true, finalState: "charging_active", source: "electrafi"},
	} {
		if code := patchPricing(t, f, `{"cost": 12.5}`); code != http.StatusNoContent {
			t.Errorf("stale/non-live open row %+v = %d, want 204", *f, code)
		}
	}
	// Just inside the window is still in progress.
	if code := patchPricing(t, &fakePricer{found: true, finalState: "charging_ready", endedAt: time.Now().Add(-29 * time.Minute)}, `{"cost": 12.5}`); code != http.StatusConflict {
		t.Errorf("29 min old open row = %d, want 409", code)
	}
	f := &fakePricer{}
	if code := patchPricing(t, f, `{"cost": 12.5}`); code != http.StatusNotFound || len(f.calls) != 0 {
		t.Errorf("missing = %d %v", code, f.calls)
	}
}

// A charge's own estimate month and link leave the API only if the store
// would have accepted them, on the list and the detail alike.
func TestDecorateChargeCleansEstimateFields(t *testing.T) {
	bad := charges.Charge{ID: "x", CostSource: "observed_estimate", EstimateMonth: "2026-13", EstimateURL: "javascript:alert(1)"}
	if got := decorateCharge(bad, settings.ChargingConfig{}, nil); got.Charge.EstimateMonth != "" || got.Charge.EstimateURL != "" {
		t.Errorf("bad values leaked: %q %q", got.Charge.EstimateMonth, got.Charge.EstimateURL)
	}
	good := charges.Charge{ID: "y", CostSource: "observed_estimate", EstimateMonth: "2026-10", EstimateURL: "https://dcfctracker.com/reports/evgo"}
	if got := decorateCharge(good, settings.ChargingConfig{}, nil); got.Charge.EstimateMonth != "2026-10" || got.Charge.EstimateURL == "" {
		t.Errorf("valid values dropped: %+v", got.Charge)
	}
}
