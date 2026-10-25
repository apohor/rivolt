package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeUnitsPatch(t *testing.T) {
	cases := []struct {
		name, body string
		bad        bool
	}{
		{"distance", `{"distance_unit":"km"}`, false},
		{"temperature", `{"temperature_unit":"f"}`, false},
		{"both", `{"distance_unit":"mi","temperature_unit":"c"}`, false},
		{"back to following distance", `{"temperature_unit":"auto"}`, false},
		{"seed", `{"seed":true,"distance_unit":"km"}`, false},
		{"empty", `{}`, true},
		{"unknown field", `{"distance_unit":"km","timezone":"UTC"}`, true},
		{"bad distance", `{"distance_unit":"miles"}`, true},
		{"bad temperature", `{"temperature_unit":"k"}`, true},
		{"distance auto is not a thing", `{"distance_unit":"auto"}`, true},
		{"seed without distance", `{"seed":true,"temperature_unit":"c"}`, true},
		{"seed with temperature", `{"seed":true,"distance_unit":"km","temperature_unit":"c"}`, true},
		{"two objects", `{"distance_unit":"km"}{"distance_unit":"mi"}`, true},
		{"not json", `distance=km`, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPatch, "/api/settings/units", strings.NewReader(c.body))
		_, bad := decodeUnitsPatch(httptest.NewRecorder(), r)
		if (bad != "") != c.bad {
			t.Errorf("%s: refused=%q, want refused=%v", c.name, bad, c.bad)
		}
	}
}

// With no settings store the read still answers (placeholder defaults, source unset) and the write says it is unavailable.
func TestUnitsHandlersWithoutAStore(t *testing.T) {
	rec := httptest.NewRecorder()
	handleUnitsGet(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/settings/units", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"distance_unit":"mi"`) || !strings.Contains(rec.Body.String(), `"distance_source":"unset"`) ||
		!strings.Contains(rec.Body.String(), `"temperature_unit":"f"`) {
		t.Errorf("GET = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleUnitsPatch(nil)(rec, httptest.NewRequest(http.MethodPatch, "/api/settings/units", strings.NewReader(`{"distance_unit":"km"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("PATCH = %d, want 503", rec.Code)
	}
}
