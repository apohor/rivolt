package api

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// The estimate comes from the open charge's own recorded energy, and a session that is not the charge's is not served.
func TestDecorateLiveSessionFor(t *testing.T) {
	cfg := settings.ChargingConfig{HomePricePerKWh: 0.11, HomeCurrency: "USD"}

	// A charge just begun: 0.2 kWh at the home rate, not a previous charge's 85 kWh. No session, so the body is empty
	// apart from the estimate and a client falls back to the truck's own power.
	r := decorateLiveSessionFor("v", nil, 0.2, cfg)
	if math.Abs(r.EstimatedCost-0.022) > 1e-9 || r.EstimatedCurrency != "USD" {
		t.Errorf("estimate %+v, want 0.022 USD", r)
	}
	// The zeroed payload: the fields are there (a client reads zeros) and none of them is a stale session's.
	raw, _ := json.Marshal(r)
	for _, want := range []string{`"power_kw":0`, `"total_charged_energy_kwh":0`, `"active":false`, `"estimated_cost":0.022`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("response lacks %s: %s", want, raw)
		}
	}

	// Nothing recorded yet: no estimate at all.
	if r := decorateLiveSessionFor("v", nil, 0, cfg); r.EstimatedCost != 0 || r.EstimatedCurrency != "" {
		t.Errorf("an estimate with no energy: %+v", r)
	}
	// Rivian's own price stands: no estimate beside it.
	if r := decorateLiveSessionFor("v", &rivian.LiveSession{CurrentPrice: "4.20"}, 30, cfg); r.EstimatedCost != 0 {
		t.Errorf("an estimate beside a billed price: %+v", r)
	}
	// No home rate configured: no estimate.
	if r := decorateLiveSessionFor("v", nil, 5, settings.ChargingConfig{}); r.EstimatedCost != 0 {
		t.Errorf("an estimate with no rate: %+v", r)
	}
	// The bonded session is served beside the estimate.
	sess := &rivian.LiveSession{PowerKW: 7.6, TotalChargedEnergyKWh: 0.3}
	if r := decorateLiveSessionFor("v", sess, 0.3, cfg); r.LiveSession != sess || math.Abs(r.EstimatedCost-0.033) > 1e-9 {
		t.Errorf("bonded session response: %+v", r)
	}
}
