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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			noCacheIndex(w)
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		// Set no-cache on the literal index.html too (root navigations).
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
