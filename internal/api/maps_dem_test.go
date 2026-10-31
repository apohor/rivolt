package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

func configTiles(t *testing.T, extras ...configExtras) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	handleConfig(false, true, false, false, nil, nil, extras...)(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var cfg struct {
		Tiles map[string]any `json:"tiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Tiles
}

func TestConfigAdvertisesTheTerrainRouteOnlyWhenMounted(t *testing.T) {
	if got := configTiles(t); got["dem_url"] != nil {
		t.Errorf("terrain advertised with no route: %v", got)
	}
	got := configTiles(t, configExtras{DEM: true})
	if got["dem_url"] != "/api/maps/dem/{z}/{x}/{y}.png" || got["dem_minzoom"] != float64(3) || got["dem_maxzoom"] != float64(10) {
		t.Errorf("%v", got)
	}
}

func TestDemoReadsTerrainTilesAndNothingElseUnderDEM(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/maps/dem/9/100/200.png":       true,
		"/api/maps/dem/13/0/0.png":          false,
		"/api/maps/dem/9/100/200.jpg":       false,
		"/api/maps/dem/../tiles/us.pmtiles": false,
		"/api/maps/dem/":                    false,
		"/api/maps/dem":                     false,
		"/api/maps/demx/9/1/1.png":          false,
	} {
		if got := demoAllowed(http.MethodGet, p, "rv-test-1"); got != want {
			t.Errorf("GET %s = %v, want %v", p, got, want)
		}
	}
	if demoAllowed(http.MethodPost, "/api/maps/dem/9/100/200.png", "rv-test-1") {
		t.Error("the demo may POST to the terrain route")
	}
}

// Through the real router: signed in it answers a tile with the cache header, every other path and method is refused before the mirror, and
// signed out it is not reachable.
func TestTerrainRouteThroughTheRouter(t *testing.T) {
	hits := 0
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG"))
	})
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, DEMProxy: upstream})
	get := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader(""))
		req.RemoteAddr = "203.0.113.9:4000"
		h.ServeHTTP(rec, req)
		return rec
	}
	in := asSession(router, "")
	rec := get(in, http.MethodGet, "/api/maps/dem/9/100/200.png")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "private, max-age=86400" || hits != 1 {
		t.Fatalf("signed in: %d %q hits=%d", rec.Code, rec.Header().Get("Cache-Control"), hits)
	}
	for _, c := range []struct{ m, p string }{
		{http.MethodGet, "/api/maps/dem/13/0/0.png"}, {http.MethodGet, "/api/maps/dem/9/100/200.jpg"}, {http.MethodGet, "/api/maps/dem/"},
		{http.MethodPost, "/api/maps/dem/9/100/200.png"}, {http.MethodDelete, "/api/maps/dem/9/100/200.png"},
	} {
		if rec := get(in, c.m, c.p); rec.Code == 200 {
			t.Errorf("%s %s answered 200", c.m, c.p)
		}
	}
	if hits != 1 {
		t.Errorf("a refused request reached the mirror (%d hits)", hits)
	}
	if rec := get(router, http.MethodGet, "/api/maps/dem/9/100/200.png"); rec.Code == 200 || hits != 1 {
		t.Errorf("signed out: %d hits=%d", rec.Code, hits)
	}
}
