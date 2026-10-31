package maps

import (
	"net/http"
	"path"
	"strconv"
	"strings"
)

// The DEM (terrain) tile route: Terrarium PNG tiles served from the in-cluster elevation mirror, same-origin, so the web map's hillshade
// never asks a third party for terrain. The mirror is a static nginx tree {z}/{x}/{y}.png; this is the allowlist and response guard in
// front of it.

// DEMMaxZoom is the deepest zoom the route admits (the recorder's own mirror holds z=12 for Texas); DEMMinZoom/DEMClientMaxZoom are what the
// client is told to request (the hillshade needs the coarse levels everywhere and nothing finer than z10).
const (
	DEMMaxZoom       = 12
	DEMClientMinZoom = 3
	DEMClientMaxZoom = 10
)

// MaxDEMResponse is the most a DEM tile may carry: a Terrarium tile is under 100 KB; a larger answer is the upstream serving something else.
const MaxDEMResponse = 512 << 10

// DEMTile parses "/z/x/y.png" strictly (no leading zeros, no extra segments or characters, x and y inside the zoom's grid).
func DEMTile(p string) (z, x, y int, ok bool) {
	if path.Clean(p) != p || !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, ".png") {
		return 0, 0, 0, false
	}
	parts := strings.Split(strings.TrimSuffix(p[1:], ".png"), "/")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var n [3]int
	for i, s := range parts {
		if s == "" || len(s) > 8 || (len(s) > 1 && s[0] == '0') {
			return 0, 0, 0, false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, 0, 0, false
			}
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, 0, 0, false
		}
		n[i] = v
	}
	z, x, y = n[0], n[1], n[2]
	if z > DEMMaxZoom || x >= 1<<z || y >= 1<<z {
		return 0, 0, 0, false
	}
	return z, x, y, true
}

// DEMPath reports whether p (the path after /api/maps/dem) names one tile.
func DEMPath(p string) bool { _, _, _, ok := DEMTile(p); return ok }

// AllowDEM admits only GET/HEAD of a single tile path, and guards the answer: PNG only, bounded, with no cookies and a private cache header
// the upstream's own does not override. A tile the mirror does not hold is a 404 (the client draws no terrain there), cached briefly.
func AllowDEM(next http.Handler) http.Handler {
	return allow(guardDEM(next), func(r *http.Request) bool {
		return (r.Method == http.MethodGet || r.Method == http.MethodHead) && DEMPath(r.URL.Path)
	})
}

func guardDEM(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range conditionalHeaders {
			r.Header.Del(h)
		}
		r.Header.Del("Range") // a tile is served whole
		next.ServeHTTP(&demWriter{ResponseWriter: w}, r)
	})
}

type demWriter struct {
	http.ResponseWriter
	wroteHeader, refused bool
	written              int64
}

func (g *demWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *demWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	h := g.Header()
	h.Del("Set-Cookie")
	switch {
	case code == http.StatusOK:
		cl, _ := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
		if ct := strings.ToLower(h.Get("Content-Type")); !strings.HasPrefix(ct, "image/png") || cl > MaxDEMResponse {
			g.refuse()
			return
		}
		h.Set("Cache-Control", "private, max-age=86400")
		h.Set("X-Content-Type-Options", "nosniff")
	case code == http.StatusNotFound:
		for k := range h {
			delete(h, k)
		}
		h.Set("Cache-Control", "private, max-age=300")
		h.Set("Content-Type", "text/plain; charset=utf-8")
		g.ResponseWriter.WriteHeader(http.StatusNotFound)
		return
	case code/100 == 3 || code/100 == 4 || code/100 == 5:
		g.refuse()
		return
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *demWriter) refuse() {
	g.refused = true
	h := g.Header()
	for k := range h {
		delete(h, k)
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	g.ResponseWriter.WriteHeader(http.StatusBadGateway)
	_, _ = g.ResponseWriter.Write([]byte("terrain tile server did not answer a tile\n"))
}

func (g *demWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.refused {
		return 0, errTileRefused
	}
	if g.written += int64(len(b)); g.written > MaxDEMResponse {
		panic(http.ErrAbortHandler)
	}
	return g.ResponseWriter.Write(b)
}
