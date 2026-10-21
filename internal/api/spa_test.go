package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":           {Data: []byte("<html>shell</html>")},
		"manifest.webmanifest": {Data: []byte("{}")},
		"sw.js":                {Data: []byte("//sw")},
		"assets/app-abc.js":    {Data: []byte("//js")},
	}
	h := spaHandler(fsys)
	cases := []struct {
		path   string
		status int
		body   string
	}{
		// Real files resolve.
		{"/robots.txt", 200, "User-agent: *\nDisallow: /\n"},
		{"/sitemap.xml", 404, ""},
		{"/manifest.webmanifest", 200, "{}"},
		{"/sw.js", 200, "//sw"},
		{"/assets/app-abc.js", 200, "//js"},
		// App routes still get the shell.
		{"/", 200, "<html>shell</html>"},
		{"/privacy", 200, "<html>shell</html>"},
		{"/terms", 200, "<html>shell</html>"},
		{"/demo", 200, "<html>shell</html>"},
		{"/demo/drives/123", 200, "<html>shell</html>"},
		{"/signup/full", 200, "<html>shell</html>"},
		// Missing files are real 404s, not the shell.
		{"/og.png", 404, ""},
		{"/favicon.ico", 404, ""},
		{"/assets/missing-abc.js", 404, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.status)
			continue
		}
		if c.body != "" && rec.Body.String() != c.body {
			t.Errorf("%s: body %q, want %q", c.path, rec.Body.String(), c.body)
		}
	}
}

// A missing asset is a 404 nothing keeps: during a rolling update a browser
// can ask an old pod for the new build's bundle, and a cached 404 (Cloudflare
// keeps one for a static extension for minutes) would outlive the rollout.
// Existing assets stay cacheable.
func TestMissingAssetNotCached(t *testing.T) {
	h := spaHandler(fstest.MapFS{
		"index.html":        {Data: []byte("<html>shell</html>")},
		"assets/app-abc.js": {Data: []byte("//js")},
	})
	for _, p := range []string{"/assets/index-NEW123.js", "/assets/index-NEW123.css", "/.well-known/nothing"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d Cache-Control=%q", p, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app-abc.js", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") == "no-store" {
		t.Fatalf("an existing asset: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}
