package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
)

func postLayers(h http.HandlerFunc, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/maps/corridor-layers", bytes.NewReader(b)))
	return rec
}

// An empty archive answers with an empty list (not null), a zero total and no cursor; a long polyline is thinned, not refused.
func TestCorridorLayersAnswersAnEmptyCorridorAndTakesALongRoute(t *testing.T) {
	h := handleCorridorLayers(emptyArchive(t))
	for _, n := range []int{100, 5001, 150_000} {
		rec := postLayers(h, map[string]any{"path": longRoute(n), "kinds": []string{"dcfc", "hotels", "camp"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("%d points: %d %q", n, rec.Code, rec.Body.String())
		}
		var out corridorLayersResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Count != 0 || out.Total != 0 || out.NextAfterKm != nil {
			t.Errorf("%d points: %+v %v", n, out, err)
		}
		if !strings.Contains(rec.Body.String(), `"pois":[]`) {
			t.Errorf("an empty result must be [] and not null: %s", rec.Body.String())
		}
	}
}

func TestCorridorLayersRefusesBadInput(t *testing.T) {
	h := handleCorridorLayers(emptyArchive(t))
	route := longRoute(100)
	for name, body := range map[string]map[string]any{
		"no kinds":      {"path": route},
		"unknown kind":  {"path": route, "kinds": []string{"dcfc", "nope"}},
		"all is a kind": {"path": route, "kinds": []string{"all"}},
		"one point":     {"path": [][2]float64{{1, 2}}, "kinds": []string{"dcfc"}},
		"too long":      {"path": longRoute(chargers.MaxRoutePoints + 1), "kinds": []string{"dcfc"}},
	} {
		if rec := postLayers(h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	if rec := postLayers(handleCorridorLayers(nil), map[string]any{"path": route, "kinds": []string{"dcfc"}}); rec.Code != http.StatusNotFound {
		t.Errorf("no archive: %d", rec.Code)
	}
}
