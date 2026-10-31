package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// Unhashed root files get a short explicit TTL, the service worker no-cache,
// index.html stays uncached (DA on #786).
func TestRootStaticFilesShortCache(t *testing.T) {
	h := spaHandler(fstest.MapFS{
		"index.html":        {Data: []byte("<html>shell</html>")},
		"asset-guard.js":    {Data: []byte("//guard")},
		"theme-init.js":     {Data: []byte("//theme")},
		"sw.js":             {Data: []byte("//sw")},
		"favicon.svg":       {Data: []byte("<svg/>")},
		"assets/app-abc.js": {Data: []byte("//js")},
	})
	for p, want := range map[string]string{
		"/asset-guard.js?v=1": "public, max-age=300",
		"/theme-init.js":      "public, max-age=300",
		"/favicon.svg":        "public, max-age=300",
		"/sw.js":              "no-cache",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != want {
			t.Errorf("%s: %d Cache-Control=%q, want %q", p, w.Code, w.Header().Get("Cache-Control"), want)
		}
	}
}

func TestHashedAssetsImmutable(t *testing.T) {
	const immutable = "public, max-age=31536000, immutable"
	fsys := fstest.MapFS{
		"index.html":                     {Data: []byte("<html>shell</html>")},
		"assets/index-BW3_JwXA.js":       {Data: []byte("//js")},
		"assets/jsx-runtime-Wy9s6hX0.js": {Data: []byte("//js")},
		"assets/index-a-B_c9xZ.css":      {Data: []byte("a{}")},
		"assets/unhashed.js":             {Data: []byte("//js")},
		"assets/short-abc.js":            {Data: []byte("//js")},
		"theme-init-BW3_JwXA.js":         {Data: []byte("//not under assets/")},
	}
	// Through the real router, so no middleware overrides the header.
	r := New(Deps{WebFS: fsys})
	for p, want := range map[string]string{
		"/assets/index-BW3_JwXA.js":       immutable,
		"/assets/jsx-runtime-Wy9s6hX0.js": immutable,
		"/assets/index-a-B_c9xZ.css":      immutable,
		"/assets/unhashed.js":             "public, max-age=300",
		"/assets/short-abc.js":            "public, max-age=300",
		"/theme-init-BW3_JwXA.js":         "public, max-age=300",
		"/assets/index-NOTTHERE.js":       "no-store",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if got := w.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: %d Cache-Control=%q, want %q", p, w.Code, got, want)
		}
	}
}

// The public sample-trip pages were removed: the hub and every slug under it answer a permanent redirect to the demo's Plan tab on the
// app host (so shared links and crawlers land somewhere real), and nothing else changes.
func TestRemovedSampleTripPagesRedirectToTheDemoPlanTab(t *testing.T) {
	h := spaHandler(fstest.MapFS{"index.html": {Data: []byte("<html>shell</html>")}})
	for _, p := range []string{"/sample-trips", "/sample-trips/", "/sample-trips/la-las-vegas-two-days", "/sample-trips/anything-else", "/sample-trips/x/"} {
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRequest(m, "http://rivolt.dev"+p, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusMovedPermanently {
				t.Errorf("%s %s: status %d, want 301", m, p, w.Code)
			}
			if got := w.Header().Get("Location"); got != "https://demo.rivolt.dev/demo/trips/plan" {
				t.Errorf("%s %s: Location %q", m, p, got)
			}
		}
	}
	// Only that tree, and only reads: a POST is not redirected, and a path that merely starts the same way is not caught.
	for _, c := range []struct{ m, p string }{{http.MethodPost, "/sample-trips"}, {http.MethodGet, "/sample-tripsx"}, {http.MethodGet, "/sample"}} {
		r := httptest.NewRequest(c.m, "http://rivolt.dev"+c.p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusMovedPermanently {
			t.Errorf("%s %s was redirected", c.m, c.p)
		}
	}
	// Neither the sitemap nor llms.txt names them any more.
	if sm, ok := sitemapXML(hostApp); !ok || strings.Contains(sm, "sample-trips") {
		t.Errorf("sitemap still lists sample trips: %q", sm)
	}
	if llms, _ := llmsTxt(hostApp); strings.Contains(llms, "sample-trips") || strings.Contains(llms, "Sample road trips") {
		t.Error("llms.txt still lists sample trips")
	}
}

// Through the real router (the one main builds), with full URLs: no earlier route or host middleware may intercept the old sample-trip
// addresses, and the demo host must not redirect them (standing rule since #569: path behaviour is tested through the router).
func TestRemovedSampleTripPagesRedirectThroughTheRealRouter(t *testing.T) {
	r := New(Deps{WebFS: fstest.MapFS{"index.html": {Data: []byte("<html>shell</html>")}}})
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}
	for _, u := range []string{
		"https://rivolt.dev/sample-trips/la-las-vegas-two-days",
		"https://rivolt.dev/sample-trips",
		"https://rivolt.dev/sample-trips/?utm_source=forum",
	} {
		w := get(u)
		if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "https://demo.rivolt.dev/demo/trips/plan" {
			t.Errorf("%s: %d Location=%q, want a 301 to the demo Plan tab (no query forwarded)", u, w.Code, w.Header().Get("Location"))
		}
	}
	if w := get("https://demo.rivolt.dev/sample-trips"); w.Code == http.StatusMovedPermanently {
		t.Errorf("the demo host redirected /sample-trips: %q", w.Header().Get("Location"))
	}
}
