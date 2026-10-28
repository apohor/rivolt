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
		"seattle-portland":       {1, 0, false},
		"denver-moab":            {1, 1, false},
		"sf-tahoe":               {1, 1, false},
		"la-phoenix":             {1, 2, false},
		"nyc-boston":             {1, 0, false},
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
		// Plausible coordinates: every route point lies near the trip's own endpoints and stops (a decoding sanity check, whatever the
		// region), within a degree of their box.
		lo, hi := tr.Route[0], tr.Route[0]
		grow := func(lat, lon float64) {
			lo[0], lo[1] = min(lo[0], lat), min(lo[1], lon)
			hi[0], hi[1] = max(hi[0], lat), max(hi[1], lon)
		}
		grow(tr.Route[len(tr.Route)-1][0], tr.Route[len(tr.Route)-1][1])
		for _, d := range tr.Days {
			for _, st := range d.Stops {
				grow(st.Lat, st.Lon)
			}
		}
		for _, pt := range tr.Route {
			if pt[0] < lo[0]-1 || pt[0] > hi[0]+1 || pt[1] < lo[1]-1 || pt[1] > hi[1]+1 {
				t.Fatalf("%s: point %v outside the trip's own box %v..%v", p.Slug, pt, lo, hi)
			}
		}
		if tr.TZ == "" || ZoneOf(tr).String() != tr.TZ {
			t.Errorf("%s: tz %q", p.Slug, tr.TZ)
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
