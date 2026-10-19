package demo

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
)

// The public sample-trip pages (rivolt.dev/sample-trips/<slug>): each slug maps
// to one of the embedded preset trips, with the copy for its page. The copy lives
// in sampletrips.json so it can be edited as plain text.
//
//go:embed sampletrips.json
var sampleTripsJSON []byte

// SampleTripCopy is one page: its slug, the preset it shows and its copy.
type SampleTripCopy struct {
	Slug   string `json:"slug"`
	Preset string `json:"preset"`
	// MapPresets are the presets whose route polylines are drawn on the page's
	// map, in order. Empty means just Preset. A multi-day preset carries no
	// polyline of its own, so it names the single-leg presets it is made of.
	MapPresets  []string `json:"map_presets,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Intro       string   `json:"intro"`
}

// SampleTripHub is the copy for the index page.
type SampleTripHub struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Intro       string `json:"intro"`
}

// SampleTripSet is the hub plus its pages, in display order.
type SampleTripSet struct {
	Hub   SampleTripHub    `json:"hub"`
	Trips []SampleTripCopy `json:"trips"`
}

var sampleSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var (
	sampleOnce sync.Once
	sampleSet  SampleTripSet
	sampleErr  error
)

// SampleTrips returns the page table. It fails if the file is malformed, a slug is
// repeated or not URL-safe, a field is empty, or a page names a preset that is not
// embedded: a bad table must stop the build's tests, not 404 in production.
func SampleTrips() (SampleTripSet, error) {
	sampleOnce.Do(func() { sampleSet, sampleErr = readSampleTrips() })
	return sampleSet, sampleErr
}

func readSampleTrips() (SampleTripSet, error) {
	var set SampleTripSet
	if err := json.Unmarshal(sampleTripsJSON, &set); err != nil {
		return SampleTripSet{}, fmt.Errorf("demo sample trips: %w", err)
	}
	if err := validateSampleTrips(set); err != nil {
		return SampleTripSet{}, err
	}
	return set, nil
}

// validateSampleTrips checks a table (see SampleTrips).
func validateSampleTrips(set SampleTripSet) error {
	if set.Hub.Title == "" || set.Hub.Description == "" || set.Hub.Intro == "" {
		return fmt.Errorf("demo sample trips: the hub needs a title, description and intro")
	}
	seen := map[string]bool{}
	for _, t := range set.Trips {
		if !sampleSlugPattern.MatchString(t.Slug) || len(t.Slug) > 60 {
			return fmt.Errorf("demo sample trips: bad slug %q", t.Slug)
		}
		if seen[t.Slug] {
			return fmt.Errorf("demo sample trips: duplicate slug %q", t.Slug)
		}
		seen[t.Slug] = true
		if t.Title == "" || t.Description == "" || t.Intro == "" {
			return fmt.Errorf("demo sample trips: %q needs a title, description and intro", t.Slug)
		}
		for _, id := range append([]string{t.Preset}, t.MapPresets...) {
			if _, ok := PresetPlan(id); !ok {
				return fmt.Errorf("demo sample trips: %q names unknown preset %q", t.Slug, id)
			}
		}
	}
	if len(set.Trips) == 0 {
		return fmt.Errorf("demo sample trips: no trips")
	}
	return nil
}
