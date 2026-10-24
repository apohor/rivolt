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
	r := decorateLiveSessionFor("v", nil, rivian.ChargeNow{EnergyKWh: 0.2, PeakKW: 7.6}, cfg)
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
	if r := decorateLiveSessionFor("v", nil, rivian.ChargeNow{}, cfg); r.EstimatedCost != 0 || r.EstimatedCurrency != "" {
		t.Errorf("an estimate with no energy: %+v", r)
	}
	// Rivian's own price stands: no estimate beside it.
	if r := decorateLiveSessionFor("v", &rivian.LiveSession{CurrentPrice: "4.20"}, rivian.ChargeNow{EnergyKWh: 30, PeakKW: 7.6}, cfg); r.EstimatedCost != 0 {
		t.Errorf("an estimate beside a billed price: %+v", r)
	}
	// No home rate configured: no estimate.
	if r := decorateLiveSessionFor("v", nil, rivian.ChargeNow{EnergyKWh: 5, PeakKW: 7.6}, settings.ChargingConfig{}); r.EstimatedCost != 0 {
		t.Errorf("an estimate with no rate: %+v", r)
	}
	// The bonded session is served beside the estimate.
	sess := &rivian.LiveSession{PowerKW: 7.6, TotalChargedEnergyKWh: 0.3}
	if r := decorateLiveSessionFor("v", sess, rivian.ChargeNow{EnergyKWh: 0.3, PeakKW: 7.6}, cfg); r.LiveSession != sess || math.Abs(r.EstimatedCost-0.033) > 1e-9 {
		t.Errorf("bonded session response: %+v", r)
	}
}

// The home rate prices only a charge it is fair for: an AC charge not known to be away. A fast charge on another network and
// an AC charge somewhere that is not home have a price of their own, so they get no estimate (unless Rivian sent one).
func TestLiveSessionEstimateIsForHomeAndACChargesOnly(t *testing.T) {
	cfg := settings.ChargingConfig{HomePricePerKWh: 0.11, HomeCurrency: "USD"}
	for _, c := range []struct {
		name string
		now  rivian.ChargeNow
		want bool
	}{
		{"home AC", rivian.ChargeNow{EnergyKWh: 5, PeakKW: 7.6, Place: "home"}, true},
		{"AC, place unknown", rivian.ChargeNow{EnergyKWh: 5, PeakKW: 7.6}, true},
		{"AC away from home", rivian.ChargeNow{EnergyKWh: 5, PeakKW: 7.6, Place: "away"}, false},
		{"a fast charge on another network", rivian.ChargeNow{EnergyKWh: 30, PeakKW: 150}, false},
		{"a fast charge at home", rivian.ChargeNow{EnergyKWh: 30, PeakKW: 150, Place: "home"}, false},
		{"a charge with no power seen yet", rivian.ChargeNow{EnergyKWh: 0.1}, true},
	} {
		got := decorateLiveSessionFor("v", nil, c.now, cfg).EstimatedCost > 0
		if got != c.want {
			t.Errorf("%s: estimate %v, want %v", c.name, got, c.want)
		}
	}
	// Rivian's own price on a fast charge still stands, with no estimate beside it.
	r := decorateLiveSessionFor("v", &rivian.LiveSession{CurrentPrice: "12.40", CurrentCurrency: "USD"}, rivian.ChargeNow{EnergyKWh: 30, PeakKW: 150}, cfg)
	if r.EstimatedCost != 0 || r.CurrentPrice != "12.40" {
		t.Errorf("billed fast charge: %+v", r)
	}
}
