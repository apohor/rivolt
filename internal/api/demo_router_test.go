package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
)

// TestDemoRouter_NoAnonymousWritesAndNoAIOrUpstream is the key
// invariant of the public demo: against the real router, with the demo
// on and auth enforced, an anonymous demo visitor cannot reach ANY route
// that is not a GET/HEAD (the demo user would otherwise write as the
// demo user), and cannot reach the planner compute, AI, geocode or
// routing routes by any method. Every registered route is exercised.
func TestDemoRouter_NoAnonymousWritesAndNoAIOrUpstream(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true})
	walker, ok := router.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose its routes")
	}
	never := []string{
		"/api/trips/plan", "/api/trips/plan-multiday", "/api/trips/plan/advice", "/api/trips/plan/raw",
		"/api/trips/plan/diag", "/api/geocode", "/api/maps/valhalla", "/api/maps/chargers-along",
		"}/efficiency", "/api/live-drive", // the AI drive recap (/api/drives/{id}/efficiency), not the aggregate insight
	}
	vin := demo.VehicleID()
	checked, mutating := 0, 0
	err = chi.Walk(walker, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := strings.NewReplacer("{vehicleID}", vin, "{id}", uuid.NewString(), "/*", "/x").Replace(route)
		for strings.Contains(path, "{") { // any other URL parameter
			i := strings.Index(path, "{")
			j := strings.Index(path[i:], "}")
			path = path[:i] + "x" + path[i+j+1:]
		}
		if !strings.HasPrefix(route, "/api/") {
			return nil // the SPA/static catch-all, not the API
		}
		// Public by design and not acting as the demo user: logout only
		// clears the caller's own cookie.
		if route == "/api/auth/logout" {
			return nil
		}
		isRead := method == http.MethodGet || method == http.MethodHead
		isNever := false
		for _, n := range never {
			if strings.Contains(route, n) {
				isNever = true
			}
		}
		if isRead && !isNever {
			return nil // reads are bounded by demoAllowed (table-tested)
		}
		checked++
		if !isRead {
			mutating++
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.RemoteAddr = "203.0.113.9:4000"
		router.ServeHTTP(rec, req)
		if rec.Header().Get(demoHeader) != "" || (rec.Code >= 200 && rec.Code < 300) || rec.Code >= 500 {
			t.Errorf("anonymous %s %s -> %d (demo header %q): reachable", method, route, rec.Code, rec.Header().Get(demoHeader))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutating < 20 {
		t.Fatalf("only %d mutating routes found: the walk is not seeing the router", mutating)
	}
	t.Logf("checked %d routes (%d mutating)", checked, mutating)
}
