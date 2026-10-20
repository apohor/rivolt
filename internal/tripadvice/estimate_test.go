package tripadvice

import (
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/rivian"
)

func stop(name string, from, to float64) rivian.PlannedWaypoint {
	return rivian.PlannedWaypoint{WaypointType: "DC_CHARGE_STATION", Name: name, ArrivalSoC: from, DepartureSoC: to}
}

func route(ws ...rivian.PlannedWaypoint) *rivian.TripRoute {
	return &rivian.TripRoute{Waypoints: append([]rivian.PlannedWaypoint{{WaypointType: "ORIGIN"}}, append(ws, rivian.PlannedWaypoint{WaypointType: "DESTINATION"})...)}
}

var oct2026 = time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)

func TestEstimateRouteUsesNoTableRate(t *testing.T) {
	// A network with no observed figure and no own price: no dollar number at all.
	c := EstimateRoute(route(stop("IONNA Rechargery - Barstow", 10, 60)), Context{PackKWh: 100, Now: oct2026})
	if c.PricedStops != 0 || c.UnpricedStops != 1 || c.DCFCSpendLow != 0 || c.DCFCSpendHigh != 0 || c.EstimateBasis != "none" {
		t.Errorf("cost = %+v, want the stop unpriced and no spend", c)
	}
	if len(c.Breakdown) != 1 || c.Breakdown[0].Price != nil || c.Breakdown[0].CostLow != nil {
		t.Errorf("breakdown = %+v, want a stop with no price", c.Breakdown)
	}
}

func TestEstimateRouteObservedPriceCarriesItsSource(t *testing.T) {
	c := EstimateRoute(route(stop("EVgo - Mojave", 10, 60)), Context{PackKWh: 100, Now: oct2026}) // 50 kWh
	if c.PricedStops != 1 || c.EstimateBasis != BasisObserved {
		t.Fatalf("cost = %+v", c)
	}
	p := c.Breakdown[0].Price
	if p == nil || p.Basis != BasisObserved || p.Source != "DCFC Tracker" || p.ObservedMonth != "2026-10" || !strings.HasPrefix(p.SourceURL, "https://dcfctracker.com/") {
		t.Fatalf("price = %+v", p)
	}
	if want := "Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ."; p.Label != want {
		t.Errorf("label = %q", p.Label)
	}
	if got := *c.Breakdown[0].CostLow; got < 31.7 || got > 31.9 { // 50 kWh x 0.636
		t.Errorf("cost = %v, want about 31.80", got)
	}
}

func TestEstimateRouteTeslaIsARangeWithBothEndsNamed(t *testing.T) {
	c := EstimateRoute(route(stop("Tesla Supercharger - Kettleman", 10, 60)), Context{PackKWh: 100, Now: oct2026})
	p := c.Breakdown[0].Price
	if p == nil || !(p.PerKWhLow < p.PerKWhHigh) || p.LowNote == "" || p.HighNote == "" {
		t.Fatalf("price = %+v, want a range with both ends named", p)
	}
	if !(c.DCFCSpendLow < c.DCFCSpendHigh) {
		t.Errorf("spend %v..%v, want a range", c.DCFCSpendLow, c.DCFCSpendHigh)
	}
}

func TestEstimateRouteMixedAndPartial(t *testing.T) {
	c := EstimateRoute(route(
		stop("Electrify America - Primm", 10, 60),
		stop("IONNA Rechargery - Barstow", 10, 60),
		stop("EVgo - Mojave", 10, 60),
	), Context{PackKWh: 100, Now: oct2026, OwnRates: map[string]OwnRate{"evgo": {PerKWh: 0.55, Sessions: 4}}})
	if c.PricedStops != 2 || c.UnpricedStops != 1 || c.EstimateBasis != "mixed" {
		t.Fatalf("cost = %+v, want 2 priced, 1 unpriced, mixed", c)
	}
	if b := c.Breakdown[2].Price; b == nil || b.Basis != BasisYourReceipts || b.Sessions != 4 || b.Label != "Your average over your last 4 sessions" {
		t.Errorf("evgo price = %+v, want the user's own receipts to win over the observed figure", b)
	}
	if c.Breakdown[1].Price != nil {
		t.Errorf("ionna price = %+v, want none", c.Breakdown[1].Price)
	}
}

func TestEstimateRouteHidesAnExpiredObservation(t *testing.T) {
	c := EstimateRoute(route(stop("EVgo - Mojave", 10, 60)), Context{PackKWh: 100, Now: time.Date(2027, 10, 2, 0, 0, 0, 0, time.UTC)})
	if c.PricedStops != 0 || c.Breakdown[0].Price != nil {
		t.Errorf("a 12-month-old observation still priced the stop: %+v", c.Breakdown[0].Price)
	}
}

func TestEstimateRouteObservedPricesAreUSDOnly(t *testing.T) {
	c := EstimateRoute(route(stop("EVgo - Mojave", 10, 60)), Context{PackKWh: 100, Now: oct2026, HomeCurrency: "CAD"})
	if c.PricedStops != 0 {
		t.Errorf("a USD observation priced a CAD trip: %+v", c.Breakdown[0].Price)
	}
}

func TestEstimateRouteUserSetRateBeatsObservedButTheSeededDefaultDoesNot(t *testing.T) {
	def := func() dcfcrates.NetworkOverride { // the row Settings seeds: the table's own numbers
		n := dcfcrates.Networks[0] // ea
		o := dcfcrates.NetworkOverride{Slug: n.Slug, Name: n.DisplayName, GuestRate: n.GuestRate}
		if n.MemberRate != nil {
			o.MemberRate = *n.MemberRate
		}
		return o
	}
	seeded := def()
	c := EstimateRoute(route(stop("Electrify America - Primm", 10, 60)), Context{PackKWh: 100, Now: oct2026, DCFCNetworks: []dcfcrates.NetworkOverride{seeded}})
	if p := c.Breakdown[0].Price; p == nil || p.Basis != BasisObserved {
		t.Errorf("a row still holding the table default priced the stop as %+v, want the observed figure, not the dated table", p)
	}
	edited := def()
	edited.GuestRate = 0.51
	c = EstimateRoute(route(stop("Electrify America - Primm", 10, 60)), Context{PackKWh: 100, Now: oct2026, DCFCNetworks: []dcfcrates.NetworkOverride{edited}})
	if p := c.Breakdown[0].Price; p == nil || p.Basis != BasisYourSetting || p.PerKWhLow != 0.51 || p.Label != "Your saved rate" {
		t.Errorf("price = %+v, want the user's own saved 0.51", p)
	}
	edited.MemberRate, edited.MemberActive = 0.40, true
	c = EstimateRoute(route(stop("Electrify America - Primm", 10, 60)), Context{PackKWh: 100, Now: oct2026, DCFCNetworks: []dcfcrates.NetworkOverride{edited}})
	if p := c.Breakdown[0].Price; p == nil || p.PerKWhLow != 0.40 {
		t.Errorf("price = %+v, want the user's active membership rate 0.40", p)
	}
}

func TestEstimateRouteWithoutAPackSizeQuotesNothing(t *testing.T) {
	c := EstimateRoute(route(stop("EVgo - Mojave", 10, 60)), Context{Now: oct2026})
	if c.PricedStops != 0 || len(c.Breakdown) != 0 || c.EstimateBasis != "" {
		t.Errorf("cost = %+v, want nothing without a pack size", c)
	}
}
