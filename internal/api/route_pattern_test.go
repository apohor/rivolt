package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRoutePatternFromContext(t *testing.T) {
	if got := RoutePatternFromContext(context.Background()); got != "none" {
		t.Errorf("off a request: %q, want none", got)
	}
	var got string
	r := chi.NewRouter()
	r.Get("/api/vehicles/{vehicleID}/now", func(w http.ResponseWriter, req *http.Request) {
		got = RoutePatternFromContext(req.Context())
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/vehicles/veh-123/now", nil))
	if got != "/api/vehicles/{vehicleID}/now" {
		t.Errorf("pattern %q: want the route pattern, never the path with its id", got)
	}
	var nf string
	r.NotFound(func(w http.ResponseWriter, req *http.Request) { nf = RoutePatternFromContext(req.Context()) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/nope/veh-123", nil))
	if nf != "unmatched" {
		t.Errorf("a 404: %q, want unmatched", nf)
	}
}
