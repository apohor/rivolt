package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/geocoding"
)

// fastArchive is hotelArchive that also knows a fast charger near the route.
type fastArchive struct {
	hotelArchive
	fast []chargers.POI
}

func (f fastArchive) QueryCorridor(p [][2]float64, flt chargers.Filter, o chargers.QueryCorridorOptions) ([]chargers.POI, error) {
	if flt == chargers.FilterDCFC {
		return f.fast, nil
	}
	return f.hotelArchive.QueryCorridor(p, flt, o)
}

type fakeLodging struct {
	list  []geocoding.Lodging
	err   error
	on    bool
	calls int
}

func (f *fakeLodging) Enabled() bool { return f.on }
func (f *fakeLodging) Lodging(context.Context, float64, float64, float64, int) ([]geocoding.Lodging, error) {
	f.calls++
	return f.list, f.err
}

// A day with no listed-charger stay in range gets the OpenStreetMap hotels with the nearest fast charger and the OSM credit; the
// existing fields keep their meaning, and with no lodging source (or with a listed stay) nothing of it appears.
func TestOvernightHandlerFallsBackToLodgingWithAFastCharger(t *testing.T) {
	noLoad := func(context.Context) error { return nil }
	arch := fastArchive{fast: []chargers.POI{{Name: "Example Fast", EntityID: "ev-1", Lat: 34 + 805/111.195, Lon: -118.02, IsDCFC: true, MaxPowerKW: 150}}}
	lodging := &fakeLodging{on: true, list: []geocoding.Lodging{{Name: "Example Motor Inn", Lat: 34 + 800/111.195, Lon: -118.01, ID: "osm:node:42", Locality: "Exampleton"}}}
	body := map[string]any{"path": straight(1000), "drive_sec": 36000, "budget_hours": 8, "kinds": []string{"hotel", "campground"}}

	rec := postOvernight(overnightHandlerWith(arch, nil, lodging, noLoad), body)
	var out struct {
		Candidates    []any  `json:"candidates"`
		Source        string `json:"source"`
		Attribution   string `json:"attribution"`
		SourceURL     string `json:"attribution_source_url"`
		LicenseURL    string `json:"attribution_license_url"`
		FallbackStays []struct {
			Name            string `json:"name"`
			ID              string `json:"id"`
			NoOnsiteCharger bool   `json:"no_onsite_charger"`
			FastCharger     struct {
				Name      string  `json:"name"`
				EntityID  string  `json:"entity_id"`
				DistanceM float64 `json:"distance_m"`
			} `json:"fast_charger"`
		} `json:"fallback_stays"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(out.Candidates) != 0 || len(out.FallbackStays) != 1 {
		t.Fatalf("candidates %d fallback %d: %s", len(out.Candidates), len(out.FallbackStays), rec.Body.String())
	}
	s := out.FallbackStays[0]
	if s.Name != "Example Motor Inn" || s.ID != "osm:node:42" || !s.NoOnsiteCharger || s.FastCharger.Name != "Example Fast" || s.FastCharger.EntityID != "ev-1" || s.FastCharger.DistanceM <= 0 {
		t.Errorf("fallback stay %+v", s)
	}
	if out.Attribution != "© OpenStreetMap contributors" || !strings.Contains(out.SourceURL, "openstreetmap.org") || !strings.Contains(out.LicenseURL, "opendatacommons.org") || !strings.Contains(out.Source, "OpenStreetMap") {
		t.Errorf("credit: %q %q %q / %q", out.Attribution, out.SourceURL, out.LicenseURL, out.Source)
	}

	// No lodging source: no fallback, and the response keeps its shape.
	rec = postOvernight(overnightHandlerWith(arch, nil, nil, noLoad), body)
	if strings.Contains(rec.Body.String(), "fallback_stays") || strings.Contains(rec.Body.String(), "OpenStreetMap") {
		t.Errorf("without a lodging source: %s", rec.Body.String())
	}
	// Photon down: the same, and no error.
	rec = postOvernight(overnightHandlerWith(arch, nil, &fakeLodging{on: true, err: errors.New("photon: HTTP 502")}, noLoad), body)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "fallback_stays") {
		t.Errorf("Photon down: %d %s", rec.Code, rec.Body.String())
	}
	// A stay with a listed charger is in range: Photon is never asked.
	listed := fastArchive{hotelArchive: hotelArchive{pois: []chargers.POI{{Name: "Roadside Inn", EntityID: "e1", Lat: 34 + 800/111.195, Lon: -118.003, IsL2: true, MaxPowerKW: 7.2, FacilityType: "HOTEL"}}}, fast: arch.fast}
	l2 := &fakeLodging{on: true, list: lodging.list}
	rec = postOvernight(overnightHandlerWith(listed, nil, l2, noLoad), body)
	if l2.calls != 0 || strings.Contains(rec.Body.String(), "fallback_stays") || !strings.Contains(rec.Body.String(), "Roadside Inn") {
		t.Errorf("with a listed stay: lookups %d, %s", l2.calls, rec.Body.String())
	}
}
