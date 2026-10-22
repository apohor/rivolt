package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/settings"
)

func TestHomeLocationValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		h    settings.HomeLocation
		ok   bool
	}{
		{"a precise home", settings.HomeLocation{Set: true, Latitude: 32.776665, Longitude: -96.796989, Source: settings.HomeSourceCoordinates}, true},
		{"the edges", settings.HomeLocation{Set: true, Latitude: -90, Longitude: 180}, true},
		{"a clear", settings.HomeLocation{Set: false, Latitude: 999}, true},
		{"latitude past 90", settings.HomeLocation{Set: true, Latitude: 90.0001, Longitude: 0}, false},
		{"longitude past 180", settings.HomeLocation{Set: true, Latitude: 0, Longitude: -180.5}, false},
		{"not a number", settings.HomeLocation{Set: true, Latitude: math.NaN(), Longitude: 0}, false},
		{"infinite", settings.HomeLocation{Set: true, Latitude: 10, Longitude: math.Inf(1)}, false},
		{"an unknown source", settings.HomeLocation{Set: true, Latitude: 10, Longitude: 10, Source: "gps"}, false},
	} {
		if err := settings.ValidateHomeLocation(c.h); (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", c.name, err, c.ok)
		}
	}
	// JSON can't carry NaN, so the handler's 400 is for out-of-range values (before anything is read or written).
	w := httptest.NewRecorder()
	handleHomeLocationPut(nil, nil)(w, httptest.NewRequest("PUT", "/api/settings/home-location", strings.NewReader(`{"set":true,"latitude":91,"longitude":10}`)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "latitude must be between") {
		t.Errorf("out of range: %d %s", w.Code, w.Body.String())
	}
}

// The audit line says what changed by name, never a value.
func TestHomeChangedFieldsNameNoValues(t *testing.T) {
	old := settings.HomeLocation{Set: true, Latitude: 32.7, Longitude: -96.8, Label: "Home", Address: "Dallas, Texas", Source: "city"}
	h := settings.HomeLocation{Set: true, Latitude: 32.776665, Longitude: -96.796989, Label: "Home", Source: "coordinates"}
	got := strings.Join(homeChangedFields(old, h), ",")
	if got != "latitude,longitude,address,source" {
		t.Errorf("fields = %s", got)
	}
	if strings.ContainsAny(got, "0123456789") {
		t.Errorf("a value leaked into the audit fields: %s", got)
	}
}

// The address search is Photon only: 404 without it, the address only in the request body (never the URL we're
// called on), and a bare 502 when Photon fails (its error carries the address in the URL).
func TestHomeAddressSearchIsPhotonOnly(t *testing.T) {
	w := httptest.NewRecorder()
	handleHomeAddressSearch(geocoding.NewPhotonClient(""))(w, httptest.NewRequest("POST", "/api/settings/home-location/search", strings.NewReader(`{"q":"1 Main St"}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("without Photon: %d", w.Code)
	}

	var mu sync.Mutex
	var got []string
	photon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Query().Get("q"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[{"geometry":{"coordinates":[-96.797,32.7767]},"properties":{"name":"Main Street","housenumber":"1","street":"Main Street","city":"Dallas","state":"Texas","country":"United States","countrycode":"us"}}]}`))
	}))
	defer photon.Close()
	w = httptest.NewRecorder()
	handleHomeAddressSearch(geocoding.NewPhotonClient(photon.URL))(w, httptest.NewRequest("POST", "/api/settings/home-location/search", strings.NewReader(`{"q":"1 Main St, Dallas"}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Dallas") {
		t.Fatalf("with Photon: %d %s", w.Code, w.Body.String())
	}
	if len(got) != 1 || got[0] != "1 Main St, Dallas" {
		t.Errorf("Photon was asked %v", got)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer down.Close()
	w = httptest.NewRecorder()
	handleHomeAddressSearch(geocoding.NewPhotonClient(down.URL))(w, httptest.NewRequest("POST", "/api/settings/home-location/search", strings.NewReader(`{"q":"1 Main St, Dallas"}`)))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "Main") {
		t.Errorf("Photon down: %d %s", w.Code, w.Body.String())
	}
	// Too short: nothing is sent.
	w = httptest.NewRecorder()
	handleHomeAddressSearch(geocoding.NewPhotonClient(photon.URL))(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"q":"1 "}`)))
	if w.Body.String() != "[]\n" || len(got) != 1 {
		t.Errorf("short query: %q, %d calls", w.Body.String(), len(got))
	}
}
