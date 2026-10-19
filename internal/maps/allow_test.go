package maps

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowlists(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	cases := []struct {
		name   string
		h      func(http.Handler) http.Handler
		method string
		path   string
		want   int
	}{
		{"route", AllowValhalla, "POST", "/route", 204},
		{"trace", AllowValhalla, "POST", "/trace_route", 204},
		{"route GET", AllowValhalla, "GET", "/route", 404},
		{"isochrone", AllowValhalla, "POST", "/isochrone", 404},
		{"matrix", AllowValhalla, "POST", "/sources_to_targets", 404},
		{"expansion", AllowValhalla, "POST", "/expansion", 404},
		{"status", AllowValhalla, "GET", "/status", 404},
		{"traversal", AllowValhalla, "POST", "/route/../isochrone", 404},
		{"pmtiles", AllowTiles, "GET", "/us.pmtiles", 204},
		{"pmtiles head", AllowTiles, "HEAD", "/chargers.pmtiles", 204},
		{"pmtiles post", AllowTiles, "POST", "/us.pmtiles", 404},
		{"other file", AllowTiles, "GET", "/index.html", 404},
		{"listing", AllowTiles, "GET", "/", 404},
		{"nested", AllowTiles, "GET", "/a/us.pmtiles", 404},
		{"dotdot", AllowTiles, "GET", "/../etc/x.pmtiles", 404},
		{"bare suffix", AllowTiles, "GET", "/.pmtiles", 404},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://x"+c.path, nil)
		rec := httptest.NewRecorder()
		c.h(ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.path, rec.Code, c.want)
		}
	}
}
