package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/rivian"
)

// The admin raw probe must not hand out the navigation topics: a destination
// never leaves the decoder, even to an operator.
func TestParallaxRawProbeRefusesNavTopics(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/parallax-raw/{vehicleID}", handleParallaxRawProbe(rivian.NewLive()))
	for _, q := range []string{
		"navigation.navigation_service.trip_info",
		"dynamics.vehicle.gnss,navigation.navigation_service.trip_progress",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/parallax-raw/veh-1?rvms="+q, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot be probed") {
			t.Errorf("rvms=%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
}
