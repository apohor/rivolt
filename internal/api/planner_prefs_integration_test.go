//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// The Trip planner settings: GET fills in every default; a PUT that names only the two old fields (an older client) leaves the new ones alone;
// a value outside the fixed choices is a 400 that writes nothing (not even the old fields in the same body); the prefs are the user's own.
func TestPlannerPrefsSettingsRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "pp-a")
	b, _ := db.EnsureUser(ctx, pool, "pp-b")
	sf := settings.NewFactory(pool)
	sa, sb := sf.For(a), sf.For(b)

	get := func(s *settings.Store) (string, map[string]any) {
		rec := httptest.NewRecorder()
		handlePlannerPrefsGet(s)(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Body.String(), m
	}
	put := func(s *settings.Store, body string) int {
		rec := httptest.NewRecorder()
		handlePlannerPrefsPut(s)(rec, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(body)))
		return rec.Code
	}

	raw, m := get(sa)
	if m["corridor"] != "normal" || m["start_exclude_mi"] != float64(0) || m["min_fast_kw"] != float64(0) || m["preferred_only"] != false ||
		m["weather_on"] != true || m["radar_on"] != true || !strings.Contains(raw, `"layers_on":[]`) {
		t.Fatalf("defaults: %s", raw)
	}

	if c := put(sa, `{"drive_mode":"SPORT","corridor":"wide","start_exclude_mi":50,"min_fast_kw":150,"layers_on":["dcfc","hotels"],"preferred_only":true,"weather_on":false,"radar_on":false}`); c != 200 {
		t.Fatalf("full put: %d", c)
	}
	// An older client sends only the two fields it knows: the rest stay.
	if c := put(sa, `{"drive_mode":"WINTER","has_adapter":true}`); c != 200 {
		t.Fatalf("old client put: %d", c)
	}
	raw, m = get(sa)
	if m["drive_mode"] != "WINTER" || m["corridor"] != "wide" || m["start_exclude_mi"] != float64(50) || m["min_fast_kw"] != float64(150) ||
		m["preferred_only"] != true || m["weather_on"] != false || m["radar_on"] != false || !strings.Contains(raw, `"layers_on":["dcfc","hotels"]`) {
		t.Errorf("after an old client's PUT: %s", raw)
	}
	// No layer on is a real choice, not a reset to the default.
	if c := put(sa, `{"layers_on":[]}`); c != 200 {
		t.Fatalf("empty layers: %d", c)
	}
	if raw, _ = get(sa); !strings.Contains(raw, `"layers_on":[]`) {
		t.Errorf("empty layers_on lost: %s", raw)
	}

	// Outside the fixed choices: 400, and nothing in the body is written.
	for _, bad := range []string{
		`{"drive_mode":"SPORT","corridor":"huge"}`, `{"start_exclude_mi":10}`, `{"min_fast_kw":100}`,
		`{"layers_on":["dcfc","dcfc"]}`, `{"layers_on":["nope"]}`,
	} {
		before, _ := get(sa)
		if c := put(sa, bad); c != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, c)
		}
		if after, _ := get(sa); after != before {
			t.Errorf("%s wrote something:\n%s\n%s", bad, before, after)
		}
	}

	// Another user is untouched.
	if _, m = get(sb); m["corridor"] != "normal" || m["radar_on"] != true {
		t.Errorf("another user's prefs changed: %v", m)
	}
}
