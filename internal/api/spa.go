package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler serves static assets from fsys; for non-asset paths (no dot in last
// segment) it returns index.html so client-side routing works. A missing path
// whose last segment has a file extension (/robots.txt, /og.png, /x.js) is a
// real 404, not the app shell: crawlers and unfurlers would otherwise read
// HTML as the file.
//
// Cache policy: index.html (and SPA route fallthroughs) get
// "Cache-Control: no-cache" so the browser revalidates each load and
// picks up new bundle hashes after a deploy. The hashed asset files
// under /assets/* keep their long max-age — their filenames change
// every build, so caching them aggressively is correct. Without the
// no-cache on the index, browsers were holding stale HTML for
// minutes/hours after a deploy, leaving users on the previous bundle.
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	noCacheIndex := func(w http.ResponseWriter) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
	}
	// shell serves index.html for an app route. On public pages of an
	// indexable host the head is rewritten (see seo.go); every other
	// route, and every other host, is marked noindex.
	shell := func(w http.ResponseWriter, r *http.Request) {
		noCacheIndex(w)
		host := requestHost(r)
		page, canon, ok := seoLookup(host, r.URL.Path)
		if !ok {
			w.Header().Set("X-Robots-Tag", "noindex")
		}
		if ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if b, err := fs.ReadFile(fsys, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.Method == http.MethodGet {
					_, _ = w.Write(injectSEO(b, host, page, canon))
				}
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(robotsTxt(requestHost(r))))
			return
		case "/sitemap.xml":
			body, ok := sitemapXML(requestHost(r))
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = w.Write([]byte(body))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			if hasFileExtension(p) {
				http.NotFound(w, r)
				return
			}
			// Path not found — assume SPA route and serve index.html.
			shell(w, r)
			return
		}
		// Root navigations get the shell (and its SEO head) too.
		if r.URL.Path == "/" {
			shell(w, r)
			return
		}
		if p == "index.html" {
			noCacheIndex(w)
		}
		fileServer.ServeHTTP(w, r)
	})
}

// hasFileExtension reports whether the last path segment contains a dot,
// i.e. the request names a file rather than a client-side route.
func hasFileExtension(p string) bool {
	return strings.Contains(p[strings.LastIndex(p, "/")+1:], ".")
}
