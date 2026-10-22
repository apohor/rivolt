package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVehicleFilterParam(t *testing.T) {
	for raw, want := range map[string]int{
		"":                                       http.StatusOK,
		"vehicle_id=01-000000000":                http.StatusOK,
		"vehicle_id=" + strings.Repeat("a", 129): http.StatusBadRequest,
		"vehicle_id=%3Cscript%3E":                http.StatusBadRequest,
		"vehicle_id=a%27%20OR%201%3D1":           http.StatusBadRequest,
	} {
		w := httptest.NewRecorder()
		_, ok := vehicleFilter(w, httptest.NewRequest(http.MethodGet, "/api/drives?"+raw, nil))
		got := http.StatusOK
		if !ok {
			got = w.Code
		}
		if got != want {
			t.Errorf("%q: %d, want %d", raw, got, want)
		}
	}
}
