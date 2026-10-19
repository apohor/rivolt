package demo

import (
	"strings"
	"testing"
)

func TestSampleTripsTableIsValid(t *testing.T) {
	set, err := SampleTrips()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"la-las-vegas-two-days":  "919ca13a-123f-5423-994d-12d7ccd31319",
		"pasadena-san-francisco": "san-francisco",
		"la-mammoth-lakes":       "mammoth",
	}
	if len(set.Trips) != len(want) {
		t.Fatalf("%d pages, want %d", len(set.Trips), len(want))
	}
	for _, p := range set.Trips {
		if want[p.Slug] != p.Preset {
			t.Errorf("slug %q -> preset %q, want %q", p.Slug, p.Preset, want[p.Slug])
		}
		// Search snippets: a title under about 70 characters and a description under about 160 get cut otherwise.
		if len(p.Description) > 170 {
			t.Errorf("%s: description is %d characters, keep it under 170", p.Slug, len(p.Description))
		}
		for _, f := range []string{p.Title, p.Description, p.Intro} {
			if strings.Contains(strings.ToLower(f), "rivian automotive") || strings.ContainsAny(f, "<>") {
				t.Errorf("%s: copy has markup or a trademark claim: %q", p.Slug, f)
			}
		}
	}
}

func TestSampleTripsRejectBadTables(t *testing.T) {
	for name, mutate := range map[string]func(*SampleTripSet){
		"unknown preset": func(s *SampleTripSet) { s.Trips[0].Preset = "nope" },
		"duplicate slug": func(s *SampleTripSet) { s.Trips[1].Slug = s.Trips[0].Slug },
		"bad slug":       func(s *SampleTripSet) { s.Trips[0].Slug = "Has Space" },
		"empty intro":    func(s *SampleTripSet) { s.Trips[0].Intro = "" },
		"empty hub":      func(s *SampleTripSet) { s.Hub.Title = "" },
		"no trips":       func(s *SampleTripSet) { s.Trips = nil },
	} {
		set, err := SampleTrips()
		if err != nil {
			t.Fatal(err)
		}
		cp := SampleTripSet{Hub: set.Hub, Trips: append([]SampleTripCopy(nil), set.Trips...)}
		mutate(&cp)
		if err := validateSampleTrips(cp); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
