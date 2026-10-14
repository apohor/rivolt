package api

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/ledger"
)

func TestClassifyCharge(t *testing.T) {
	cfg := ledger.Config{Currency: "USD", HomeRate: 0.11, Home: &ledger.LatLon{Lat: 30.27, Lon: -97.74}}
	st := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	base := charges.Charge{ID: "x", StartedAt: st, EndedAt: st.Add(time.Hour), EnergyAddedKWh: 10,
		MaxPowerKW: 7, Lat: 30.2701, Lon: -97.7401, Currency: "USD"}
	dc := base
	dc.MaxPowerKW, dc.EnergyAddedKWh, dc.EndedAt = 150, 50, st.Add(30*time.Minute)
	cases := []struct {
		name             string
		c                charges.Charge
		src, place, kind string
		derived          bool
	}{
		{"unpriced home AC", base, "unknown", "home", "ac", true},
		{"persisted network estimate", func() charges.Charge {
			c := dc
			c.Cost, c.CostSource, c.Lat, c.Lon = 25, "network_estimate", 35.47, -97.52
			return c
		}(), "network_estimate", "away", "dcfc", false},
		{"legacy home rate on DCFC", func() charges.Charge {
			c := dc
			c.Cost, c.PricePerKWh = 5.5, 0.11
			return c
		}(), "home_rate_misapplied", "home", "dcfc", true},
		{"user free", func() charges.Charge {
			c := base
			c.CostSource = "user"
			return c
		}(), "user", "home", "ac", false},
	}
	for _, tc := range cases {
		var r chargeResponse
		classifyCharge(&r, tc.c, cfg)
		if r.CostSource != tc.src || r.Place != tc.place || r.Kind != tc.kind || r.CostSourceDerived != tc.derived {
			t.Errorf("%s: got %s/%s/%s derived=%v", tc.name, r.CostSource, r.Place, r.Kind, r.CostSourceDerived)
		}
	}
}
