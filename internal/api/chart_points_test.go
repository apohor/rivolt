package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

func chartGet(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handleChartPoints(nil, nil)(w, httptest.NewRequest(http.MethodGet, "/api/chart-points"+query, nil))
	return w
}

// Without stores the answer is two empty lists (never null), uncached.
func TestChartPointsEmpty(t *testing.T) {
	w := chartGet(t, "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, cache %q", w.Code, w.Header().Get("Cache-Control"))
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["drives"]) != "[]" || string(got["charges"]) != "[]" {
		t.Errorf("body = %s", w.Body.String())
	}
}

// A bad time, a backwards window, one wider than 400 days and a bad vehicle id are
// 400s before anything is read.
func TestChartPointsRefusesBadWindows(t *testing.T) {
	now := time.Now().UTC()
	f := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	for name, q := range map[string]string{
		"bad since":   "?since=yesterday",
		"bad until":   "?until=tomorrow",
		"backwards":   "?since=" + f(0) + "&until=" + f(-time.Hour),
		"too wide":    "?since=" + f(-401*24*time.Hour) + "&until=" + f(0),
		"bad vehicle": "?vehicle_id=a%20b",
	} {
		if w := chartGet(t, q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	if w := chartGet(t, "?since="+f(-399*24*time.Hour)+"&until="+f(0)+"&vehicle_id=rv-1"); w.Code != http.StatusOK {
		t.Errorf("a 399-day window: %d", w.Code)
	}
}

// Through the real router with full URLs (the #569 lesson): the route is mounted
// under /api, needs a signed-in user, takes the query as written, and the window
// and vehicle checks answer before anything is read.
func TestChartPointsThroughTheRouter(t *testing.T) {
	r := New(Deps{})
	uid := uuid.New()
	get := func(url string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if signedIn {
			req = req.WithContext(auth.WithUser(req.Context(), uid))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := get("https://rivolt.dev/api/chart-points", false); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", w.Code)
	}
	if w := get("https://rivolt.dev/api/chart-points?vehicle_id=rv-1&since=2026-10-01T00:00:00Z&until=2026-10-20T00:00:00Z", true); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"drives":[]`) || !strings.Contains(w.Body.String(), `"charges":[]`) {
		t.Errorf("signed in: %d %s", w.Code, w.Body.String())
	}
	for name, u := range map[string]string{
		"bad time":    "https://rivolt.dev/api/chart-points?since=nope",
		"too wide":    "https://rivolt.dev/api/chart-points?since=2024-01-01T00:00:00Z&until=2026-01-01T00:00:00Z",
		"bad vehicle": "https://rivolt.dev/api/chart-points?vehicle_id=a%20b",
	} {
		if w := get(u, true); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
}
