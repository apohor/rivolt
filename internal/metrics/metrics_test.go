package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestHTTPMiddlewareUsesRoutePattern is the load-bearing assertion
// for the metrics design: the `route` label must be the chi route
// pattern (low cardinality), NEVER the raw URL (unbounded). If chi
// upgrades break that assumption this test fails loudly before the
// metric pipeline DOSes itself in prod.
func TestHTTPMiddlewareUsesRoutePattern(t *testing.T) {
	m := New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware)
	r.Get("/state/{vehicleID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/state/abc-123", nil)
	r.ServeHTTP(rec, req.WithContext(context.Background()))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Scrape the registry through the same handler the scraper sees.
	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()

	// Must contain the pattern, not the raw vehicle ID.
	if !strings.Contains(body, `route="/state/{vehicleID}"`) {
		t.Errorf("expected route pattern label in metrics, got body:\n%s", body)
	}
	if strings.Contains(body, "abc-123") {
		t.Errorf("raw URL leaked into metric labels (cardinality bomb): %s", body)
	}
}

func TestHTTPMiddlewareUnmatchedRoute(t *testing.T) {
	m := New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware)
	// One real route so chi's middleware chain runs even for misses
	// — without any registered route chi short-circuits before the
	// chain executes, which is fine in practice (the real app
	// always has routes mounted) but not what we're testing here.
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope/whatever", nil))

	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()

	if !strings.Contains(body, `route="unmatched"`) {
		t.Errorf("expected unmatched route bucket, got body:\n%s", body)
	}
}
