package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/geocoding"
)

func photonStub(t *testing.T, status int, body string) *geocoding.PhotonClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return geocoding.NewPhotonClient(srv.URL)
}

func geocodeGet(h http.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=austin", nil))
	return w
}

// Without the operator's Open-Meteo switch, a Photon failure or no Photon at all answers "unavailable"; nothing is sent to a third party.
func TestGeocodeWithoutFallbackIsUnavailable(t *testing.T) {
	for name, photon := range map[string]*geocoding.PhotonClient{
		"photon down":    photonStub(t, 500, "boom"),
		"photon unwired": nil,
	} {
		w := geocodeGet(handleGeocode(photon, false))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "place search unavailable") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// An empty Photon answer is an empty list, not an error and not a fallback.
func TestGeocodeEmptyPhotonIsAnEmptyList(t *testing.T) {
	w := geocodeGet(handleGeocode(photonStub(t, 200, `{"features":[]}`), false))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
