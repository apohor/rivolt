package api

import (
	"encoding/json"
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

func demoOnlyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	t.Setenv("RIVOLT_DEMO_ONLY", "true")
	t.Setenv("RIVOLT_APP_URL", "https://rivolt.dev/")
}

func TestDemoOnlyPublicSet(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/config", true},
		{"HEAD", "/api/health", true},
		{"GET", "/api/demo", true},
		{"GET", "/api/demo/trips", true},
		{"GET", "/api/demo/trips/san-diego/plan", true},
		{"GET", "/api/demo/trips/a/b/plan", false},
		{"GET", "/api/demo/trips//plan", false},
		{"GET", "/api/charges", true},
		{"GET", "/api/vehicles/" + demoVeh + "/drain", true},
		{"GET", "/api/maps/tiles/us.pmtiles", true},
		{"GET", "/api/maps/dem/9/100/200.png", true},
		{"GET", "/api/maps/dem/13/0/0.png", false},
		// everything an account or write surface would be
		{"POST", "/api/config", false},
		{"POST", "/api/auth/logout", false},
		{"GET", "/api/auth/me", false},
		{"GET", "/api/auth/hydra/login", false},
		{"GET", "/api/auth/oidc/google/start", false},
		{"POST", "/api/signup", false},
		{"POST", "/api/signup/request", false},
		{"GET", "/api/signup/token/abc", false},
		{"POST", "/api/events", false},
		{"GET", "/api/admin/users", false},
		{"POST", "/api/trips/plan", false},
		{"GET", "/api/geocode", false},
		{"POST", "/api/maps/valhalla/route", false},
		{"GET", "/api/vehicles/owned", false},
		{"GET", "/api/vehicles/other/drain", false},
		{"GET", "/api/push/vapid", false},
		{"GET", "/api", false},
	}
	for _, c := range cases {
		if got := demoOnlyPublic(c.method, c.path, demoVeh); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestDemoOnlyRouter_404sEverythingOutsideTheDemo(t *testing.T) {
	demoOnlyEnv(t)
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true})
	walker := router.(chi.Routes)
	vin := demo.VehicleID()
	checked := 0
	err = chi.Walk(walker, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		path := strings.NewReplacer("{vehicleID}", vin, "{id}", uuid.NewString(), "/*", "/x").Replace(route)
		for strings.Contains(path, "{") {
			i := strings.Index(path, "{")
			j := strings.Index(path[i:], "}")
			path = path[:i] + "x" + path[i+j+1:]
		}
		if demoOnlyPublic(method, path, vin) {
			return nil
		}
		checked++
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.RemoteAddr = "203.0.113.9:4000"
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s -> %d, want 404", method, route, rec.Code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 40 {
		t.Fatalf("only %d non-demo routes checked: the walk is not seeing the router", checked)
	}
	// Even a request carrying a session cookie gets nothing on the
	// account surface.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "rivolt_session", Value: "x"})
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("/api/auth/me with a cookie = %d", rec.Code)
	}
}

func TestDemoOnlyConfig(t *testing.T) {
	get := func() map[string]any {
		h := handleConfig(false, true, true, false, nil, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
		var out struct {
			Features map[string]any `json:"features"`
			AI       map[string]any `json:"ai"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		out.Features["ai_enabled"] = out.AI["enabled"]
		return out.Features
	}
	// Off by default: the fields keep today's meaning.
	t.Setenv("RIVOLT_APP_URL", "https://rivolt.dev")
	f := get()
	if f["demo_only"] != false {
		t.Errorf("demo_only = %v without RIVOLT_DEMO_ONLY", f["demo_only"])
	}
	if _, ok := f["app_url"]; ok {
		t.Errorf("app_url leaked outside the standalone demo: %v", f["app_url"])
	}
	// On: demo_only, app_url, and no accounts, events, landing or AI.
	demoOnlyEnv(t)
	t.Setenv("RIVOLT_LANDING", "true")
	t.Setenv("RIVOLT_EVENTS", "true")
	f = get()
	if f["demo_only"] != true || f["app_url"] != "https://rivolt.dev" || f["demo_enabled"] != true {
		t.Errorf("features = %v", f)
	}
	if f["signup_enabled"] != false || f["landing_enabled"] != false || f["events_enabled"] != false || f["ai_enabled"] != false {
		t.Errorf("demo-only left accounts/events/landing/AI on: %v", f)
	}
	if f["trip_planner_enabled"] != true {
		t.Errorf("planner flag = %v, want true (presets need no flag)", f["trip_planner_enabled"])
	}
	// A bad app url is dropped, not echoed.
	t.Setenv("RIVOLT_APP_URL", "javascript:alert(1)")
	if f := get(); f["app_url"] != nil {
		t.Errorf("bad app_url served: %v", f["app_url"])
	}
}
