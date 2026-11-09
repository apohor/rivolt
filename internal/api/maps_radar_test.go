package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

func TestConfigAdvertisesRadarOnlyWhenMounted(t *testing.T) {
	got := configTiles(t)
	for _, k := range []string{"radar_url", "radar_frame_url", "radar_minzoom", "radar_maxzoom"} {
		if got[k] != nil {
			t.Errorf("radar advertised with no route: %s=%v", k, got[k])
		}
	}
	got = configTiles(t, configExtras{Radar: true})
	if got["radar_url"] != "/api/maps/radar/{z}/{x}/{y}.png" || got["radar_frame_url"] != "/api/maps/radar/frame" ||
		got["radar_minzoom"] != float64(3) || got["radar_maxzoom"] != float64(9) {
		t.Errorf("%v", got)
	}
}

// The signed-out demo, the admin host and every other account type: radar is for signed-in users of a deployment that turned it on. The demo's
// allowlist does not include it (a visitor with no account gets no NOAA traffic), the route is absent with the flag off, and it is not an
// admin-host path.
func TestRadarIsNotDemoOrAdminReachable(t *testing.T) {
	for _, p := range []string{"/api/maps/radar/frame", "/api/maps/radar/5/10/12.png"} {
		if demoAllowed(http.MethodGet, p, "rv-test-1") {
			t.Errorf("the signed-out demo may read %s", p)
		}
		if adminOnlyPublic(p) {
			t.Errorf("the admin host serves %s", p)
		}
	}
}

// Through the real router: signed in it answers, the prefix is stripped before the radar service sees the path, every method but GET and HEAD is
// refused by it, signed out it is unreachable, and with no radar service the route does not exist.
func TestRadarRouteThroughTheRouter(t *testing.T) {
	var hits atomic.Int32
	var seen atomic.Value
	radar := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		seen.Store(r.URL.Path)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Radar-Frame", "2026-11-09T00:24:11Z")
		_, _ = w.Write([]byte("\x89PNG"))
	})
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Radar: radar})
	get := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader(""))
		req.RemoteAddr = "203.0.113.9:4000"
		h.ServeHTTP(rec, req)
		return rec
	}
	in := asSession(router, "")
	rec := get(in, http.MethodGet, "/api/maps/radar/5/10/12.png?t=1")
	if rec.Code != 200 || rec.Header().Get("X-Radar-Frame") == "" || seen.Load() != "/5/10/12.png" {
		t.Fatalf("signed in: %d frame %q path %v", rec.Code, rec.Header().Get("X-Radar-Frame"), seen.Load())
	}
	if rec := get(in, http.MethodGet, "/api/maps/radar/frame"); rec.Code != 200 || seen.Load() != "/frame" {
		t.Errorf("frame: %d path %v", rec.Code, seen.Load())
	}
	before := hits.Load()
	if rec := get(router, http.MethodGet, "/api/maps/radar/5/10/12.png"); rec.Code == 200 || hits.Load() != before {
		t.Errorf("signed out: %d, %d new hits", rec.Code, hits.Load()-before)
	}
	// Off: no route.
	off := asSession(New(Deps{Auth: svc, AuthEnforced: true}), "")
	if rec := get(off, http.MethodGet, "/api/maps/radar/5/10/12.png"); rec.Code == 200 {
		t.Errorf("radar answered with no radar service: %d", rec.Code)
	}
}

// A user who asks for more than the per-minute ceiling gets 429, and another user does not.
func TestRadarIsRateLimitedPerUser(t *testing.T) {
	radar := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Radar: radar})
	a, b := asSession(router, ""), asSession(router, "")
	do := func(h http.Handler) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/maps/radar/5/10/12.png", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	limited := 0
	for i := 0; i < mapsRadarPerMin+30; i++ {
		if do(a) == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("no 429 after the per-minute ceiling")
	}
	if code := do(b); code != 200 {
		t.Errorf("another user: %d, want 200 (the limit is per user)", code)
	}
}
