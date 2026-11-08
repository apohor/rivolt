package rivian

import (
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

func priceRow(lat, lon, energy, peak float64, activeSec float64, span time.Duration) charges.Charge {
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	return charges.Charge{
		StartedAt: t0, EndedAt: t0.Add(span), Lat: lat, Lon: lon,
		EnergyAddedKWh: energy, MaxPowerKW: peak, ActiveSeconds: activeSec,
	}
}

func homeAt(lat, lon float64) HomeLookup {
	return func() (float64, float64, bool) { return lat, lon, true }
}

func TestPriceUnbilled(t *testing.T) {
	const homeLat, homeLon = 34.0, -118.0
	home := homeAt(homeLat, homeLon)
	homeRate := PriceLookup(func() (float64, string) { return 0.20, "USD" })

	cases := []struct {
		name       string
		row        charges.Charge
		set        PriceSettings
		wantSource string
		wantCost   float64
		wantNet    string
	}{
		{
			name:       "home L2 gets the home rate",
			row:        priceRow(homeLat, homeLon, 15, 7.2, 9000, 2*time.Hour+30*time.Minute),
			set:        PriceSettings{Home: homeRate, HomeLoc: home},
			wantSource: CostSourceHomeRate, wantCost: 3.0,
		},
		{
			name:       "no home location saved keeps the home rate on slow charging",
			row:        priceRow(40, -100, 10, 7.2, 5000, 2*time.Hour),
			set:        PriceSettings{Home: homeRate},
			wantSource: CostSourceHomeRate, wantCost: 2.0,
		},
		{
			name: "AC away from home is left unpriced",
			row:  priceRow(40.0, -100.0, 10, 7.2, 5000, 2*time.Hour),
			set:  PriceSettings{Home: homeRate, HomeLoc: home},
		},
		{
			name: "no home rate means unpriced",
			row:  priceRow(homeLat, homeLon, 10, 7.2, 5000, 2*time.Hour),
			set:  PriceSettings{HomeLoc: home},
		},
		{
			name:       "DC fast with a station rate",
			row:        priceRow(40, -100, 50, 150, 1800, 40*time.Minute),
			set:        PriceSettings{Home: homeRate, HomeLoc: home, DCFC: func(lat, lon float64) (float64, string, string, bool) { return 0.50, "USD", "Acme", true }},
			wantSource: CostSourceNetworkEstimate, wantCost: 25.0, wantNet: "Acme",
		},
		{
			name: "DC fast with no station match is not priced at the home rate",
			row:  priceRow(homeLat, homeLon, 50, 150, 1800, 40*time.Minute),
			set:  PriceSettings{Home: homeRate, HomeLoc: home, DCFC: func(lat, lon float64) (float64, string, string, bool) { return 0, "", "", false }},
		},
		{
			name: "DC fast with a priced detector verdict",
			row:  priceRow(40, -100, 50, 150, 1800, 40*time.Minute),
			set: PriceSettings{Home: homeRate, HomeLoc: home, Detect: func(lat, lon float64, ran bool, peak float64) (DCFCVerdict, bool) {
				return DCFCVerdict{Priced: true, Rate: 0.40, Currency: "USD", Network: "Net", State: "detected", Confidence: 0.9, Slug: "net"}, true
			}},
			wantSource: CostSourceNetworkEstimate, wantCost: 20.0, wantNet: "Net",
		},
		{
			name: "DC fast, verdict without a rate falls back to the typical price",
			row:  priceRow(40, -100, 50, 150, 1800, 40*time.Minute),
			set: PriceSettings{
				Detect: func(lat, lon float64, ran bool, peak float64) (DCFCVerdict, bool) {
					return DCFCVerdict{State: "detected", Confidence: 0.9, Slug: "net"}, true
				},
				Estimate: func(slug string, lat, lon float64, start time.Time) (DCFCEstimate, bool) {
					if slug != "net" {
						return DCFCEstimate{}, false
					}
					return DCFCEstimate{Rate: 0.60, Currency: "USD", Network: "Net", Month: "2026-10"}, true
				},
			},
			wantSource: CostSourceObservedEstimate, wantCost: 30.0, wantNet: "Net",
		},
		{
			name:       "a fragment with a spurious peak is not DC fast",
			row:        priceRow(homeLat, homeLon, 0.12, 145, 4, 4*time.Second),
			set:        PriceSettings{Home: homeRate, HomeLoc: home, DCFC: func(lat, lon float64) (float64, string, string, bool) { return 0.5, "USD", "Acme", true }},
			wantSource: CostSourceHomeRate, wantCost: 0.024,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := c.row
			PriceUnbilled(&row, c.set, &NetworkMemo{})
			if row.CostSource != c.wantSource {
				t.Errorf("CostSource = %q, want %q", row.CostSource, c.wantSource)
			}
			if math.Abs(row.Cost-c.wantCost) > 1e-9 {
				t.Errorf("Cost = %v, want %v", row.Cost, c.wantCost)
			}
			if row.Network != c.wantNet {
				t.Errorf("Network = %q, want %q", row.Network, c.wantNet)
			}
			if c.wantCost > 0 && math.Abs(row.PricePerKWh*row.EnergyAddedKWh-row.Cost) > 1e-9 {
				t.Errorf("PricePerKWh %v x energy != cost %v", row.PricePerKWh, row.Cost)
			}
		})
	}
}

// The memo keeps the station rate and the detector verdict between the builds of an open session.
func TestPriceUnbilledMemo(t *testing.T) {
	calls := 0
	set := PriceSettings{DCFC: func(lat, lon float64) (float64, string, string, bool) {
		calls++
		return 0.50, "USD", "Acme", true
	}}
	memo := &NetworkMemo{}
	for i := 0; i < 3; i++ {
		row := priceRow(40, -100, 50, 150, 1800, 40*time.Minute)
		PriceUnbilled(&row, set, memo)
		if row.Cost != 25 {
			t.Fatalf("build %d: cost = %v", i, row.Cost)
		}
	}
	if calls != 1 || !memo.Priced {
		t.Errorf("station lookup ran %d times, want 1 (memoised)", calls)
	}

	detects := 0
	dset := PriceSettings{Detect: func(lat, lon float64, ran bool, peak float64) (DCFCVerdict, bool) {
		detects++
		conf := 0.9
		if peak < 100 {
			conf = 0.5
		}
		return DCFCVerdict{Priced: true, Rate: 0.4, Currency: "USD", Network: "N", Confidence: conf}, true
	}}
	m2 := &NetworkMemo{}
	row := priceRow(40, -100, 50, 90, 1800, 40*time.Minute)
	PriceUnbilled(&row, dset, m2)
	PriceUnbilled(&row, dset, m2)
	if detects != 1 {
		t.Fatalf("detector ran %d times at an unchanged peak, want 1", detects)
	}
	// A low-confidence verdict is looked at again once the peak has grown more than 10%.
	row2 := priceRow(40, -100, 50, 150, 1800, 40*time.Minute)
	PriceUnbilled(&row2, dset, m2)
	if detects != 2 {
		t.Errorf("detector ran %d times after the peak grew, want 2", detects)
	}
	// Rivian's own isRivianCharger flag re-asks once.
	yes := true
	row3 := priceRow(40, -100, 50, 150, 1800, 40*time.Minute)
	row3.RivianCharger = &yes
	PriceUnbilled(&row3, dset, m2)
	PriceUnbilled(&row3, dset, m2)
	if detects != 3 {
		t.Errorf("detector ran %d times, want 3 (re-asked once when isRivianCharger arrived)", detects)
	}
}
