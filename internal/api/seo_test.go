package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func seoGet(t *testing.T, h http.Handler, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSEOInjection(t *testing.T) {
	// The real shell, so the regexes are checked against the real head.
	shell, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := spaHandler(fstest.MapFS{"index.html": {Data: shell}})

	t.Run("app host root", func(t *testing.T) {
		rec := seoGet(t, h, "rivolt.dev", "/")
		b := rec.Body.String()
		for _, want := range []string{
			`<link rel="canonical" href="https://rivolt.dev/" />`,
			`<meta property="og:url" content="https://rivolt.dev/" />`,
			`<noscript><h1>Rivolt</h1>`,
			`<div id="root"></div>`,
			"not affiliated with Rivian",
		} {
			if !strings.Contains(b, want) {
				t.Errorf("missing %q", want)
			}
		}
		if rec.Header().Get("X-Robots-Tag") != "" {
			t.Error("public page must not be noindex")
		}
		if strings.Count(b, "<title>") != 1 || strings.Count(b, `rel="canonical"`) != 1 {
			t.Error("title or canonical duplicated")
		}
	})
	t.Run("privacy and terms get their own title and canonical", func(t *testing.T) {
		for path, title := range map[string]string{"/privacy": "Privacy policy | Rivolt", "/terms/": "Terms of service | Rivolt"} {
			b := seoGet(t, h, "rivolt.dev", path).Body.String()
			if !strings.Contains(b, "<title>"+title+"</title>") {
				t.Errorf("%s: title not set", path)
			}
			if !strings.Contains(b, `og:title" content="`+title) || !strings.Contains(b, `twitter:title" content="`+title) {
				t.Errorf("%s: og/twitter title not set", path)
			}
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/terms/").Body.String(), `href="https://rivolt.dev/terms"`) {
			t.Error("trailing slash not normalised in canonical")
		}
	})
	t.Run("demo host is indexable with its own canonical", func(t *testing.T) {
		for _, p := range []string{"/", "/demo"} {
			rec := seoGet(t, h, "demo.rivolt.dev", p)
			b := rec.Body.String()
			if !strings.Contains(b, `href="https://demo.rivolt.dev/"`) || !strings.Contains(b, "sample truck") ||
				!strings.Contains(b, "not affiliated with Rivian") {
				t.Errorf("demo %s: wrong head", p)
			}
			if rec.Header().Get("X-Robots-Tag") != "" {
				t.Errorf("demo %s should be indexable", p)
			}
		}
	})
	t.Run("app routes are noindex and keep the generic shell", func(t *testing.T) {
		for _, p := range []string{"/login", "/signup", "/drives/abc", "/demo/activity"} {
			rec := seoGet(t, h, "rivolt.dev", p)
			if rec.Header().Get("X-Robots-Tag") != "noindex" {
				t.Errorf("%s: want noindex", p)
			}
			if strings.Contains(rec.Body.String(), `rel="canonical"`) {
				t.Errorf("%s: canonical must not be injected", p)
			}
		}
	})
	t.Run("unknown and preview hosts are noindex, even on public routes", func(t *testing.T) {
		for _, host := range []string{"preview.rivolt.dev", "localhost:8080", "192.168.1.5:8080", "self.example.org"} {
			rec := seoGet(t, h, host, "/")
			if rec.Header().Get("X-Robots-Tag") != "noindex" {
				t.Errorf("%s: want noindex", host)
			}
			if strings.Contains(rec.Body.String(), `rel="canonical"`) {
				t.Errorf("%s: no canonical on a non-public host", host)
			}
			if got := seoGet(t, h, host, "/robots.txt").Body.String(); got != "User-agent: *\nDisallow: /\n" {
				t.Errorf("%s: robots %q", host, got)
			}
			if seoGet(t, h, host, "/sitemap.xml").Code != 404 {
				t.Errorf("%s: sitemap should 404", host)
			}
		}
	})
	t.Run("robots and sitemap per indexable host", func(t *testing.T) {
		for _, host := range []string{"rivolt.dev", "demo.rivolt.dev", "RIVOLT.DEV:443"} {
			base := strings.ToLower(strings.Split(host, ":")[0])
			if cc := seoGet(t, h, host, "/robots.txt").Header().Get("Cache-Control"); cc != "public, max-age=300" {
				t.Errorf("%s: robots Cache-Control %q", host, cc)
			}
			r := seoGet(t, h, host, "/robots.txt").Body.String()
			if !strings.Contains(r, "Disallow: /api/") || !strings.Contains(r, "Sitemap: https://"+base+"/sitemap.xml") || strings.Contains(r, "Disallow: /\n") {
				t.Errorf("%s: robots %q", host, r)
			}
			sm := seoGet(t, h, host, "/sitemap.xml")
			for _, p := range []string{"/", "/privacy", "/terms"} {
				if !strings.Contains(sm.Body.String(), "<loc>https://"+base+p+"</loc>") {
					t.Errorf("%s: sitemap missing %s", host, p)
				}
			}
		}
	})
}
