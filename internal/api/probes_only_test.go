package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A role with no public routes (RIVOLT_ROLE ingest, jobs) mounts the probes and nothing else: the route list is exactly the three probes, and
// every public path, the web bundle included, is a 404 through the real router.
func TestIngestRouterNoPublicRoutes(t *testing.T) {
	h := New(Deps{ProbesOnly: true, Version: "test"})
	var routes []string
	if err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(routes)
	if want := "GET /api/health,GET /healthz,GET /readyz"; strings.Join(routes, ",") != want {
		t.Errorf("routes %v, want exactly %s", routes, want)
	}
	for _, path := range []string{"/", "/index.html", "/api/state", "/api/auth/me", "/api/vehicles", "/api/admin/users", "/assets/x.js",
		"/api/maps/radar/3/1/2.png", "/api/config", "/.well-known/apple-app-site-association"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://rivolt.test"+path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://rivolt.test/api/stripe/webhook", strings.NewReader("{}")))
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /api/stripe/webhook: %d, want 404", w.Code)
	}
	for _, path := range []string{"/healthz", "/api/health"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://rivolt.test"+path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", path, w.Code)
		}
	}
}

// The full router (RIVOLT_ROLE all, api) still mounts the public routes.
func TestFullRouterKeepsPublicRoutes(t *testing.T) {
	found := false
	_ = chi.Walk(New(Deps{}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found = found || strings.HasPrefix(route, "/api/state")
		return nil
	})
	if !found {
		t.Error("the full router has no /api/state route")
	}
}
