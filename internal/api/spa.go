package api

import (
	"io/fs"
	"net/http"
	"regexp"
	"strings"

	"github.com/apohor/rivolt/internal/iosapp"
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
// under /assets/* are immutable for a year: their filenames change
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
		host := requestHost(r)
		// Sample-trip pages (sampletrips.go): pre-rendered, with their own
		// caching; an unknown slug is a real 404, not the app's 200.
		if path := strings.TrimSuffix(r.URL.Path, "/"); host == hostApp && (path == pathSampleTrips || isSampleTripPath(path)) &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if b, err := fs.ReadFile(fsys, "index.html"); err == nil {
				if p, ok := sampleTripPage(b, path); ok {
					serveSampleTrip(w, r, p, path)
					return
				}
				noCacheIndex(w)
				w.Header().Set("X-Robots-Tag", "noindex")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				if r.Method == http.MethodGet {
					_, _ = w.Write(b)
				}
				return
			}
		}
		noCacheIndex(w)
		page, canon, ok := seoLookup(host, r.URL.Path)
		if !ok {
			w.Header().Set("X-Robots-Tag", "noindex")
		}
		if ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Add("Vary", "Accept")
			w.Header().Set("Link", linkHeader(canon))
			if wantsMarkdown(r.Header.Get("Accept")) {
				w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(markdownPage(host, page, canon)))
				}
				return
			}
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
		case "/app":
			// A short link for the share card and the emails, so the store link can change
			// without a release. 404 until RIVOLT_IOS_APP_URL is set; the target is always an
			// Apple host (iosapp.Clean), never a request value.
			link := iosapp.URL()
			if link == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, link, http.StatusFound)
			return
		case "/robots.txt":
			// Short, explicit TTL: Cloudflare otherwise caches .txt at the
			// edge for hours, so a host-rule change would linger.
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(robotsTxt(requestHost(r))))
			return
		case "/sitemap.xml":
			body, ok := sitemapXML(requestHost(r))
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = w.Write([]byte(body))
			return
		case "/llms.txt":
			body, ok := llmsTxt(requestHost(r))
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(body))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			// Missing files and missing /.well-known/ documents are real 404s.
			// Discovery documents (api-catalog, OAuth metadata, MCP cards...)
			// must not be answered with the app shell.
			if hasFileExtension(p) || strings.HasPrefix(p, ".well-known/") {
				// Never cacheable: during a rolling update a browser can ask
				// an old pod for the new build's /assets/index-<hash>.js, and
				// Cloudflare caches a bare 404 for a static extension for
				// minutes, which would break that file for everyone after the
				// rollout. no-store makes the miss one-off.
				w.Header().Set("Cache-Control", "no-store")
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
		switch {
		case p == "index.html":
			noCacheIndex(w)
		case p == "sw.js":
			// The service worker: browsers should see a new one promptly.
			w.Header().Set("Cache-Control", "no-cache")
		case hashedAsset.MatchString(p):
			// Vite's content-hashed build output: a new build gets a new
			// name, so a year and immutable is safe. Without an explicit
			// header Cloudflare applied its 4-hour default.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			// Unhashed files (asset-guard.js, theme-init.js, the manifest,
			// icons, og.png): a short, explicit TTL, so a fix ships within
			// minutes even if a ?v= bump is forgotten, and Cloudflare's
			// default edge TTL doesn't decide for us.
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// hashedAsset matches Vite's build output, assets/<name>-<8-char hash>.<ext>.
var hashedAsset = regexp.MustCompile(`^assets/[^/]+-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+$`)

// hasFileExtension reports whether the last path segment contains a dot,
// i.e. the request names a file rather than a client-side route.
func hasFileExtension(p string) bool {
	return strings.Contains(p[strings.LastIndex(p, "/")+1:], ".")
}
