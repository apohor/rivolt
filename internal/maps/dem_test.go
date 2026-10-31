package maps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDEMTilePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/3/1/2.png":        true,
		"/10/163/357.png":   true,
		"/12/839/1601.png":  true,
		"/0/0/0.png":        true,
		"/13/0/0.png":       false, // deeper than the mirror holds
		"/3/8/0.png":        false, // x outside the grid
		"/3/0/8.png":        false,
		"/03/1/2.png":       false, // leading zero
		"/3/01/2.png":       false,
		"/3/1/2.jpg":        false,
		"/3/1/2":            false,
		"/3/1.png":          false,
		"/3/1/2/3.png":      false,
		"/../3/1/2.png":     false,
		"/3/../3/1/2.png":   false,
		"//3/1/2.png":       false,
		"/3/1/2.png/":       false,
		"/3/-1/2.png":       false,
		"/3/1/2.png?x=1":    false, // a query is not part of the path, and would make this a different name
		"/3/%2e/2.png":      false,
		"/3/1\\2.png":       false,
		"/a/1/2.png":        false,
		"/3/1/99999999.png": false,
		"":                  false,
		"/":                 false,
	} {
		if got := DEMPath(p); got != want {
			t.Errorf("DEMPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func demUpstream(code int, ctype string, body []byte, hdr map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		w.WriteHeader(code)
		_, _ = w.Write(body)
	})
}

func getDEM(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestAllowDEM(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nxxxx")
	ok := AllowDEM(demUpstream(200, "image/png", png, map[string]string{"Set-Cookie": "a=b", "Cache-Control": "public, max-age=1"}))
	rec := getDEM(ok, http.MethodGet, "/9/100/200.png")
	if rec.Code != 200 || rec.Body.Len() != len(png) {
		t.Fatalf("%d %d", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Cache-Control") != "private, max-age=86400" || rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", rec.Header())
	}
	if rec := getDEM(ok, http.MethodHead, "/9/100/200.png"); rec.Code != 200 {
		t.Errorf("HEAD: %d", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := getDEM(ok, m, "/9/100/200.png"); rec.Code != 404 {
			t.Errorf("%s: %d, want refused", m, rec.Code)
		}
	}
	for _, p := range []string{"/", "/index.html", "/9/100/200.png/x", "/13/1/1.png", "/9/../9/1/1.png", "/9/100/2%2e.png"} {
		if rec := getDEM(ok, http.MethodGet, p); rec.Code != 404 {
			t.Errorf("%s: %d, want 404 before the upstream", p, rec.Code)
		}
	}
	// A conditional or range request never reaches the mirror as such.
	var seen http.Header
	AllowDEM(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})).ServeHTTP(httptest.NewRecorder(), func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/9/1/1.png", nil)
		r.Header.Set("Range", "bytes=0-9")
		r.Header.Set("If-None-Match", "x")
		return r
	}())
	if seen.Get("Range") != "" || seen.Get("If-None-Match") != "" {
		t.Errorf("the mirror saw %v", seen)
	}
}

func TestDEMGuardsTheAnswer(t *testing.T) {
	big := []byte(strings.Repeat("x", MaxDEMResponse+1))
	for name, c := range map[string]struct {
		up   http.Handler
		want int
	}{
		"not a png":               {demUpstream(200, "text/html", []byte("<html>"), nil), 502},
		"no content type":         {demUpstream(200, "", []byte("x"), nil), 502},
		"declared too large":      {demUpstream(200, "image/png", big, map[string]string{"Content-Length": "999999"}), 502},
		"upstream redirect":       {demUpstream(302, "", nil, map[string]string{"Location": "https://example.test/"}), 502},
		"upstream server error":   {demUpstream(500, "text/plain", []byte("boom"), nil), 502},
		"a tile the mirror lacks": {demUpstream(404, "text/html", []byte("not found"), map[string]string{"Set-Cookie": "a=b"}), 404},
	} {
		rec := getDEM(AllowDEM(c.up), http.MethodGet, "/9/1/1.png")
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, c.want)
		}
		if rec.Header().Get("Set-Cookie") != "" || strings.Contains(rec.Body.String(), "Location") {
			t.Errorf("%s: headers leaked: %v", name, rec.Header())
		}
	}
	rec := getDEM(AllowDEM(demUpstream(404, "text/html", nil, nil)), http.MethodGet, "/9/1/1.png")
	if rec.Header().Get("Cache-Control") != "private, max-age=300" {
		t.Errorf("a missing tile is cached for %q", rec.Header().Get("Cache-Control"))
	}
}
