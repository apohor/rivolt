package maps

import (
	"net/http"
	"path"
	"strings"
)

// AllowValhalla admits only what the SPA uses: POST /route and
// POST /trace_route. Everything else (isochrone, matrix, expansion,
// status, any GET) is refused before it reaches Valhalla.
func AllowValhalla(next http.Handler) http.Handler {
	return allow(next, func(r *http.Request) bool {
		return r.Method == http.MethodPost && (r.URL.Path == "/route" || r.URL.Path == "/trace_route")
	})
}

// AllowTiles admits only GET/HEAD of a single top-level *.pmtiles file
// (Range requests are plain GETs). Directory listings, nested paths and
// any other file on the tile server are refused.
func AllowTiles(next http.Handler) http.Handler {
	return allow(next, func(r *http.Request) bool {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return false
		}
		p := r.URL.Path
		name := strings.TrimPrefix(p, "/")
		return strings.HasSuffix(name, ".pmtiles") && len(name) > len(".pmtiles") &&
			!strings.ContainsAny(name, "/\\") && path.Clean(p) == p
	})
}

func allow(next http.Handler, ok func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok(r) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
