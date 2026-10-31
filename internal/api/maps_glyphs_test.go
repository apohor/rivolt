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

func TestConfigAdvertisesGlyphsWithTheBasemapAndNoSprite(t *testing.T) {
	if got := configTiles(t); got["glyphs_url"] != "/api/maps/glyphs/{fontstack}/{range}.pbf" {
		t.Errorf("%v", got)
	}
	for k := range configTiles(t, configExtras{DEM: true}) {
		if strings.Contains(strings.ToLower(k), "sprite") {
			t.Errorf("config advertises a sprite: %s", k)
		}
	}
}

func TestDemoReadsShippedGlyphsOnly(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/maps/glyphs/Noto Sans Regular/0-255.pbf":   true,
		"/api/maps/glyphs/Noto Sans Italic/256-511.pbf":  true,
		"/api/maps/glyphs/Noto Sans Regular/512-767.pbf": false,
		"/api/maps/glyphs/Noto Sans Bold/0-255.pbf":      false,
		"/api/maps/glyphs/../tiles/us.pmtiles":           false,
		"/api/maps/glyphs/":                              false,
		"/api/maps/glyphsx/Noto Sans Regular/0-255.pbf":  false,
	} {
		if got := demoAllowed(http.MethodGet, p, "rv-test-1"); got != want {
			t.Errorf("GET %s = %v, want %v", p, got, want)
		}
	}
	if demoMapAssetCost("/api/maps/glyphs/Noto Sans Regular/0-255.pbf") < 50_000 || demoMapAssetCost("/api/maps/dem/9/1/1.png") != demoDEMTileCost {
		t.Error("asset costs")
	}
}

// Through the real router: signed in a glyph range is served with its cache headers and revalidates; unshipped ranges, other methods and the
// sprite do not exist; signed out it is not reachable.
func TestGlyphRouteThroughTheRouter(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true})
	do := func(h http.Handler, method, path, inm string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader(""))
		req.RemoteAddr = "203.0.113.9:4000"
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	in := asSession(router, "")
	u := "/api/maps/glyphs/Noto%20Sans%20Regular/0-255.pbf"
	rec := do(in, http.MethodGet, u, "")
	if rec.Code != 200 || rec.Header().Get("ETag") == "" || !strings.HasPrefix(rec.Header().Get("Cache-Control"), "public, max-age=") || rec.Body.Len() < 10_000 {
		t.Fatalf("signed in: %d %v", rec.Code, rec.Header())
	}
	if rec2 := do(in, http.MethodGet, u, rec.Header().Get("ETag")); rec2.Code != 304 {
		t.Errorf("revalidation: %d", rec2.Code)
	}
	for _, c := range []struct{ m, p string }{
		{http.MethodGet, "/api/maps/glyphs/Noto%20Sans%20Regular/512-767.pbf"}, {http.MethodPost, u},
		{http.MethodGet, "/api/maps/sprite.json"}, {http.MethodGet, "/api/maps/sprite.png"}, {http.MethodGet, "/api/maps/sprite@2x.png"},
	} {
		if rec := do(in, c.m, c.p, ""); rec.Code == 200 {
			t.Errorf("%s %s answered 200", c.m, c.p)
		}
	}
	if rec := do(router, http.MethodGet, u, ""); rec.Code == 200 {
		t.Errorf("signed out: %d", rec.Code)
	}
	// There is no sprite route anywhere in the API.
	_ = chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(strings.ToLower(route), "sprite") {
			t.Errorf("a sprite route exists: %s %s", method, route)
		}
		return nil
	})
}
