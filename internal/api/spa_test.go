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
