package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
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

// The insight endpoints follow the account's units when the request names none; an explicit imperial or metric stays an
// override; an unrecognised value counts as absent; other query parameters survive.
func TestDefaultUnitsParam(t *testing.T) {
	uid := uuid.New()
	run := func(lookup func(context.Context, uuid.UUID) string, target string, withUser bool) url.Values {
		var got url.Values
		h := defaultUnitsParam(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Query() }))
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if withUser {
			req = req.WithContext(auth.WithUser(req.Context(), uid))
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}
	km := func(context.Context, uuid.UUID) string { return "metric" }
	mi := func(context.Context, uuid.UUID) string { return "imperial" }
	cases := []struct {
		name   string
		lookup func(context.Context, uuid.UUID) string
		target string
		user   bool
		want   string
	}{
		{"absent follows a km account", km, "/api/insights/efficiency?since=2026-10-01T00:00:00Z", true, "metric"},
		{"absent follows a mi account", mi, "/api/insights/efficiency", true, "imperial"},
		{"explicit imperial overrides a km account", km, "/api/insights/efficiency?units=imperial", true, "imperial"},
		{"explicit metric overrides a mi account", mi, "/api/insights/efficiency?units=metric", true, "metric"},
		{"an unrecognised value counts as absent", km, "/api/insights/efficiency?units=furlongs", true, "metric"},
		{"an empty value counts as absent", km, "/api/insights/efficiency?units=", true, "metric"},
		{"no signed-in user leaves the request alone", km, "/api/insights/efficiency", false, ""},
		{"no lookup leaves the request alone", nil, "/api/insights/efficiency", true, ""},
	}
	for _, c := range cases {
		if got := run(c.lookup, c.target, c.user).Get("units"); got != c.want {
			t.Errorf("%s: units=%q, want %q", c.name, got, c.want)
		}
	}
	if got := run(km, "/x?months=6&grid=1,2", true); got.Get("months") != "6" || got.Get("grid") != "1,2" || got.Get("units") != "metric" {
		t.Errorf("other parameters were lost: %v", got)
	}
}
