package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// The seven raw Rivian probes (no client uses them) are mounted only where the admin debug probes are on. Through the real router with full
// URLs (a path-matching mistake in a unit test of the handler would not show here, #569): with the flag off a signed-in request to each is
// 404 exactly like an unknown path, and no route for it is registered; with it on all seven are registered, and the neighbouring routes
// the app does use are registered either way.
var rawProbeRoutes = []string{
	"/api/state/{vehicleID}/debug", "/api/charging-schema", "/api/battery-temp/{vehicleID}", "/api/parallax-raw/{vehicleID}",
	"/api/supported-features", "/api/charging-field/{field}", "/api/charging-frames",
}

func TestRawProbeRoutesAreGatedByTheFlag(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	registered := func(h http.Handler) map[string]bool {
		got := map[string]bool{}
		_ = chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if method == http.MethodGet {
				got[strings.TrimSuffix(route, "/")] = true
			}
			return nil
		})
		return got
	}
	do := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		h.ServeHTTP(rec, req)
		return rec
	}
	neighbours := []string{"/api/state/{vehicleID}", "/api/state/{vehicleID}/fresh", "/api/live-session/{vehicleID}", "/api/live-drive/{vehicleID}"}

	off := New(Deps{Auth: svc, AuthEnforced: true})
	regOff := registered(off)
	for _, r := range rawProbeRoutes {
		if regOff[r] {
			t.Errorf("flag off: %s is registered", r)
		}
	}
	for _, r := range neighbours {
		if !regOff[r] {
			t.Errorf("flag off: %s (used by the app) is not registered: the gate caught too much", r)
		}
	}
	in := asSession(off, "")
	unknown := do(in, "/api/definitely-not-a-route").Code
	for _, p := range []string{
		"/api/state/rv-test-1/debug", "/api/charging-schema", "/api/battery-temp/rv-test-1", "/api/parallax-raw/rv-test-1?rvms=a",
		"/api/supported-features", "/api/charging-field/anything", "/api/charging-frames?vehicleID=rv-test-1",
	} {
		if rec := do(in, p); rec.Code != http.StatusNotFound || rec.Code != unknown {
			t.Errorf("flag off: GET %s = %d, want 404 like an unknown path (%d)", p, rec.Code, unknown)
		}
	}

	on := New(Deps{Auth: svc, AuthEnforced: true, AdminDebugProbes: true})
	regOn := registered(on)
	for _, r := range append(append([]string{}, rawProbeRoutes...), neighbours...) {
		if !regOn[r] {
			t.Errorf("flag on: %s is not registered", r)
		}
	}
}
