package demo

import (
	"strings"
	"testing"
)

// Every page builds from its preset, with its stops, costs and a route.
func TestSampleTrips(t *testing.T) {
	if err := SampleTripsErr(); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		days, stops int
		overnight   bool
	}{
		"la-las-vegas-two-days":  {2, 2, true},
		"pasadena-san-francisco": {1, 1, false},
		"la-mammoth-lakes":       {1, 1, false},
	}
	for _, p := range SampleTripPages {
		tr, ok := SampleTripBySlug(p.Slug)
		if !ok {
			t.Fatalf("%s didn't build", p.Slug)
		}
		w := want[p.Slug]
		if len(tr.Days) != w.days || tr.Totals.ChargeStops != w.stops || (tr.Days[0].Overnight != nil) != w.overnight {
			t.Errorf("%s: %d days, %d stops, overnight %v", p.Slug, len(tr.Days), tr.Totals.ChargeStops, tr.Days[0].Overnight != nil)
		}
		tot := tr.Totals
		if len(tr.Route) < 50 || tot.DistanceMi < 100 || tot.PricedStops+tot.UnpricedStops != tot.ChargeStops || tot.DCFCCostHigh < tot.DCFCCostLow {
			t.Errorf("%s: route %d points, %v mi, totals %+v", p.Slug, len(tr.Route), tot.DistanceMi, tot)
		}
		if tot.PricedStops > 0 && tot.DCFCCostLow <= 0 {
			t.Errorf("%s: %d stops priced but the estimate is $%v", p.Slug, tot.PricedStops, tot.DCFCCostLow)
		}
		for _, d := range tr.Days {
			for _, s := range d.Stops {
				if s.Network == "" || s.ChargeMin <= 0 {
					t.Errorf("%s: stop %+v", p.Slug, s)
				}
				// A price is an estimate with its source, or there is none: never a
				// bare dollar figure from the dated table, never $0.
				if (s.CostLow == nil) != (s.CostHigh == nil) || (s.CostLow != nil && (*s.CostLow <= 0 || *s.CostHigh < *s.CostLow || !strings.Contains(s.PriceNote, "DCFC Tracker"))) {
					t.Errorf("%s: stop price %+v", p.Slug, s)
				}
			}
		}
		// Plausible coordinates: California and Nevada.
		for _, pt := range tr.Route {
			if pt[0] < 32 || pt[0] > 39 || pt[1] < -122.6 || pt[1] > -114 {
				t.Fatalf("%s: point %v outside CA/NV", p.Slug, pt)
			}
		}
		if !strings.HasPrefix(tr.DemoURL, "https://demo.rivolt.dev/trips/plan?preset=") {
			t.Errorf("%s: demo url %s", p.Slug, tr.DemoURL)
		}
	}
	if v, _ := SampleTripBySlug("la-las-vegas-two-days"); v.Days[0].Overnight.Nights != 2 {
		t.Errorf("the Las Vegas stay spans %d nights, want 2 (Saturday to Monday)", v.Days[0].Overnight.Nights)
	}
	if _, ok := SampleTripBySlug("nope"); ok {
		t.Error("an unknown slug built")
	}
}
