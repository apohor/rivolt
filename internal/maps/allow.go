package maps

import (
	"net/http"
	"path"
	"strconv"
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
// any other file on the tile server are refused. Whatever is admitted goes
// through guardTiles, so a GET can only ever be answered with a bounded
// byte range.
func AllowTiles(next http.Handler) http.Handler {
	return allow(guardTiles(next), func(r *http.Request) bool {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return false
		}
		p := r.URL.Path
		name := strings.TrimPrefix(p, "/")
		return strings.HasSuffix(name, ".pmtiles") && len(name) > len(".pmtiles") &&
			!strings.ContainsAny(name, "/\\") && path.Clean(p) == p
	})
}

// MaxTileResponse is the most a single tile read may carry. Real reads are a
// header, a directory or one tile (the largest in the 19 GB basemap is 344 KiB);
// 2 MiB leaves room, and anything bigger is an upstream answering something other
// than the byte range that was asked for.
const MaxTileResponse = 2 << 20

// conditionalHeaders are the request headers that can change what a Range request
// returns. If-Range with a validator that does not match makes a server ignore the
// Range and send the whole file (a 19 GB 200), so none of them reaches the tile server.
var conditionalHeaders = []string{"If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"}

// guardTiles keeps a tile read to the range that was asked for: it drops the
// conditional request headers, and refuses (502) an upstream answer to a GET that is
// a 200 (the whole file) or longer than MaxTileResponse, before any of the body is
// sent. A HEAD has no body and passes.
func guardTiles(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range conditionalHeaders {
			r.Header.Del(h)
		}
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&tileGuardWriter{ResponseWriter: w}, r)
	})
}

type tileGuardWriter struct {
	http.ResponseWriter
	wroteHeader bool
	refused     bool
	written     int64
}

// Unwrap lets http.ResponseController reach the underlying writer (flushing).
func (g *tileGuardWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *tileGuardWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	h := g.Header()
	cl, _ := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	if code == http.StatusOK || (code/100 == 2 && cl > MaxTileResponse) {
		g.refused = true
		for k := range h {
			delete(h, k)
		}
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
		g.ResponseWriter.WriteHeader(http.StatusBadGateway)
		_, _ = g.ResponseWriter.Write([]byte("tile server did not answer the byte range\n"))
		return
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *tileGuardWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.refused {
		return len(b), nil // the upstream body is dropped
	}
	// Backstop for a body longer than its headers said: stop the connection
	// rather than stream more than a tile read may carry.
	if g.written += int64(len(b)); g.written > MaxTileResponse {
		panic(http.ErrAbortHandler)
	}
	return g.ResponseWriter.Write(b)
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
