package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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
	t.Run("ElectraFi page is public on the app host only", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/moving-from-electrafi").Body.String()
		if !strings.Contains(b, "<title>Moving your history from ElectraFi to Rivolt</title>") || !strings.Contains(b, `href="https://rivolt.dev/moving-from-electrafi"`) {
			t.Error("head not injected")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "<loc>https://rivolt.dev/moving-from-electrafi</loc>") {
			t.Error("not in the app sitemap")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/moving-from-electrafi") {
			t.Error("not in llms.txt")
		}
		d := seoGet(t, h, "demo.rivolt.dev", "/moving-from-electrafi")
		if d.Header().Get("X-Robots-Tag") != "noindex" || strings.Contains(seoGet(t, h, "demo.rivolt.dev", "/sitemap.xml").Body.String(), "electrafi") {
			t.Error("demo host must not index or list it")
		}
		req := httptest.NewRequest(http.MethodGet, "http://rivolt.dev/moving-from-electrafi", nil)
		req.Header.Set("Accept", "text/markdown")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") || !strings.Contains(rec.Body.String(), "Import ElectraFi CSV") {
			t.Error("markdown version missing")
		}
	})
	t.Run("connect-safety page is public on the app host only", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/safe-to-connect").Body.String()
		if !strings.Contains(b, "<title>Is it safe to connect my Rivian account to Rivolt?</title>") || !strings.Contains(b, `href="https://rivolt.dev/safe-to-connect"`) {
			t.Error("head not injected")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "https://rivolt.dev/safe-to-connect") ||
			!strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/safe-to-connect") {
			t.Error("not in sitemap and llms.txt")
		}
		if seoGet(t, h, "demo.rivolt.dev", "/safe-to-connect").Header().Get("X-Robots-Tag") != "noindex" {
			t.Error("demo host must not index it")
		}
	})
	t.Run("the charging-networks page is public on the app host only", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/charging-networks").Body.String()
		if !strings.Contains(b, "<title>Rivian fast-charging networks: plans and typical prices</title>") || !strings.Contains(b, `href="https://rivolt.dev/charging-networks"`) {
			t.Error("head not injected")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "https://rivolt.dev/charging-networks") ||
			!strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/charging-networks") {
			t.Error("not in sitemap and llms.txt")
		}
		if seoGet(t, h, "demo.rivolt.dev", "/charging-networks").Header().Get("X-Robots-Tag") != "noindex" {
			t.Error("demo host must not index it")
		}
	})
	t.Run("the R2 guide is public on the app host only", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/r2").Body.String()
		if !strings.Contains(b, "<title>Your first week with an R2 in Rivolt</title>") || !strings.Contains(b, `href="https://rivolt.dev/r2"`) {
			t.Error("head not injected")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "https://rivolt.dev/r2") ||
			!strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/r2") {
			t.Error("not in sitemap and llms.txt")
		}
		if seoGet(t, h, "demo.rivolt.dev", "/r2").Header().Get("X-Robots-Tag") != "noindex" {
			t.Error("demo host must not index it")
		}
	})
	t.Run("changelog is public on the app host only", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/changelog").Body.String()
		// The head is html.EscapeString'd, so the apostrophe is &#39;.
		if !strings.Contains(b, "<title>What&#39;s new in Rivolt: release notes</title>") || !strings.Contains(b, `href="https://rivolt.dev/changelog"`) {
			t.Error("head not injected")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "<loc>https://rivolt.dev/changelog</loc>") ||
			!strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/changelog") {
			t.Error("not in sitemap and llms.txt")
		}
		if seoGet(t, h, "demo.rivolt.dev", "/changelog").Header().Get("X-Robots-Tag") != "noindex" ||
			strings.Contains(seoGet(t, h, "demo.rivolt.dev", "/sitemap.xml").Body.String(), "changelog") {
			t.Error("demo host must not index it")
		}
	})
	t.Run("lease calculator is public on the app host only", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/lease-calculator").Body.String()
		if !strings.Contains(b, "<title>Rivian lease mileage calculator | Rivolt</title>") || !strings.Contains(b, `href="https://rivolt.dev/lease-calculator"`) {
			t.Error("head not injected")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "https://rivolt.dev/lease-calculator") ||
			!strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/lease-calculator") {
			t.Error("not in sitemap and llms.txt")
		}
		if seoGet(t, h, "demo.rivolt.dev", "/lease-calculator").Header().Get("X-Robots-Tag") != "noindex" {
			t.Error("demo host must not index it")
		}
	})
	t.Run("charging cost page is public on the app host only and carries no figure", func(t *testing.T) {
		const title = "How much does it cost to charge a Rivian at home? (worked example)"
		rec := seoGet(t, h, "rivolt.dev", "/charging-cost")
		b := rec.Body.String()
		for _, want := range []string{
			"<title>" + title + "</title>",
			`href="https://rivolt.dev/charging-cost"`,
			`"@type":"Article"`,
			"A worked example for a Rivian R1S: home charging at your own rate, what off-peak hours change, and what one fast-charge stop adds. Free calculator, no sign-in.",
		} {
			if !strings.Contains(b, want) {
				t.Errorf("missing %q", want)
			}
		}
		if rec.Header().Get("X-Robots-Tag") != "" {
			t.Error("a public page must not be noindex")
		}
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "https://rivolt.dev/charging-cost") ||
			!strings.Contains(seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String(), "https://rivolt.dev/charging-cost") {
			t.Error("not in sitemap and llms.txt")
		}
		if seoGet(t, h, "demo.rivolt.dev", "/charging-cost").Header().Get("X-Robots-Tag") != "noindex" ||
			strings.Contains(seoGet(t, h, "demo.rivolt.dev", "/sitemap.xml").Body.String(), "charging-cost") {
			t.Error("demo host must not index it")
		}
		// DA: no price or date-bound figure in anything the server writes for the
		// page (the client shows the typical price from the dated table).
		page := seoPages[hostApp][pathChargeCost]
		surfaces := []string{page.title, page.desc, page.summary, articleLD(hostApp, page, pathChargeCost)}
		llms, _ := llmsTxt(hostApp)
		for _, line := range strings.Split(llms, "\n") {
			if strings.Contains(line, pathChargeCost) {
				surfaces = append(surfaces, line)
			}
		}
		for _, sfc := range surfaces {
			if strings.Contains(sfc, "$") || regexp.MustCompile(`\d+\.\d{2}|20\d\d`).MatchString(sfc) {
				t.Errorf("a figure or date in server-rendered text: %q", sfc)
			}
		}
		md := markdownPage(hostApp, page, pathChargeCost)
		if strings.Contains(md, "$") {
			t.Errorf("a dollar figure in the markdown: %s", md)
		}
	})
	t.Run("charging cost page carries the dated tracker price as a data element, only while it is shown", func(t *testing.T) {
		defer func(old func() time.Time) { seoNow = old }(seoNow)
		at := func(y int, m time.Month, d int) func() time.Time {
			return func() time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
		}
		const el = `<script type="application/json" id="charging-cost">{"fast_price":0.482,"price_month":"2026-10"}</script>`
		seoNow = at(2026, 10, 21)
		b := seoGet(t, h, "rivolt.dev", "/charging-cost").Body.String()
		if !strings.Contains(b, el) {
			t.Fatalf("data element missing:\n%s", b)
		}
		// Only that element: not the noscript summary, the JSON-LD, llms.txt, the
		// markdown view or the sitemap.
		rest := strings.Replace(b, el, "", 1)
		if strings.Contains(rest, "0.482") || strings.Contains(rest, "2026-10") {
			t.Error("the figure or its month appears outside the data element")
		}
		md := markdownPage(hostApp, seoPages[hostApp][pathChargeCost], pathChargeCost)
		ll, _ := llmsTxt(hostApp)
		if strings.Contains(md, "0.482") || strings.Contains(ll, "0.482") || strings.Contains(seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String(), "0.482") {
			t.Error("the figure leaked into markdown, llms.txt or the sitemap")
		}
		if strings.Contains(seoGet(t, h, "demo.rivolt.dev", "/charging-cost").Body.String(), "charging-cost\"") {
			t.Error("the demo host got the element")
		}
		// The 12-month boundary: the figure of 2026-10 is shown through 2027-09-30
		// and gone from 2027-10-01.
		seoNow = at(2027, 9, 30)
		if !strings.Contains(seoGet(t, h, "rivolt.dev", "/charging-cost").Body.String(), el) {
			t.Error("element gone a day early")
		}
		seoNow = at(2027, 10, 1)
		gone := seoGet(t, h, "rivolt.dev", "/charging-cost").Body.String()
		if strings.Contains(gone, `id="charging-cost"`) || strings.Contains(gone, "0.482") {
			t.Error("element still served after the 12-month boundary")
		}
		if !strings.Contains(gone, `"@type":"Article"`) {
			t.Error("the rest of the page's head disappeared with the figure")
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

	t.Run("content signal on indexable robots only", func(t *testing.T) {
		for _, host := range []string{"rivolt.dev", "demo.rivolt.dev"} {
			if !strings.Contains(seoGet(t, h, host, "/robots.txt").Body.String(), "Content-Signal: search=yes, ai-input=yes, ai-train=no\n") {
				t.Errorf("%s: no content signal", host)
			}
		}
		if strings.Contains(seoGet(t, h, "preview.rivolt.dev", "/robots.txt").Body.String(), "Content-Signal") {
			t.Error("preview robots must stay plain Disallow")
		}
	})
	t.Run("llms.txt per host, 404 elsewhere", func(t *testing.T) {
		b := seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String()
		for _, want := range []string{"# Rivolt", "https://rivolt.dev/privacy", "https://demo.rivolt.dev/", "not affiliated with, endorsed by or sponsored by Rivian"} {
			if !strings.Contains(b, want) {
				t.Errorf("llms.txt missing %q", want)
			}
		}
		if strings.Contains(b, "/api") || strings.Contains(b, "/login") {
			t.Error("llms.txt must list public pages only")
		}
		if d := seoGet(t, h, "demo.rivolt.dev", "/llms.txt").Body.String(); !strings.Contains(d, "https://demo.rivolt.dev/terms") {
			t.Error("demo llms.txt should link its own host")
		}
		for _, host := range []string{"preview.rivolt.dev", "self.example.org"} {
			if seoGet(t, h, host, "/llms.txt").Code != 404 {
				t.Errorf("%s: llms.txt should 404", host)
			}
		}
	})
	t.Run("markdown negotiation", func(t *testing.T) {
		get := func(host, path, accept string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
			req.Header.Set("Accept", accept)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		rec := get("rivolt.dev", "/", "text/markdown")
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
			t.Fatalf("content-type %q", ct)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept") || !strings.Contains(rec.Body.String(), "# Rivolt: Rivian charging cost") ||
			!strings.Contains(rec.Body.String(), "not affiliated with Rivian") {
			t.Error("markdown body or Vary wrong")
		}
		if ct := get("rivolt.dev", "/privacy", "text/markdown, text/html;q=0.5").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
			t.Errorf("privacy: %q", ct)
		}
		for _, accept := range []string{"", "text/html,application/xhtml+xml,*/*;q=0.8", "text/markdown;q=0.5, text/html", "text/markdown;q=0"} {
			if ct := get("rivolt.dev", "/", accept).Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("accept %q should get HTML, got %q", accept, ct)
			}
		}
		// App routes and non-public hosts never get markdown.
		for _, c := range [][2]string{{"rivolt.dev", "/login"}, {"rivolt.dev", "/drives/1"}, {"preview.rivolt.dev", "/"}} {
			if ct := get(c[0], c[1], "text/markdown").Header().Get("Content-Type"); strings.HasPrefix(ct, "text/markdown") {
				t.Errorf("%s%s must not serve markdown", c[0], c[1])
			}
		}
	})
	t.Run("link headers and JSON-LD on public pages only", func(t *testing.T) {
		rec := seoGet(t, h, "rivolt.dev", "/")
		l := rec.Header().Get("Link")
		for _, want := range []string{`</sitemap.xml>; rel="sitemap"`, `</llms.txt>; rel="describedby"`} {
			if !strings.Contains(l, want) {
				t.Errorf("Link missing %s: %q", want, l)
			}
		}
		if strings.Contains(l, "api-catalog") {
			t.Error("must not advertise an api-catalog")
		}
		b := rec.Body.String()
		if !strings.Contains(b, `<script type="application/ld+json">`) || !strings.Contains(b, `"@type":"SoftwareApplication"`) ||
			!strings.Contains(b, `"isAccessibleForFree":true`) {
			t.Error("JSON-LD missing")
		}
		if strings.Contains(b, "aggregateRating") || strings.Contains(b, `"review"`) {
			t.Error("no ratings or reviews")
		}
		if strings.Contains(seoGet(t, h, "rivolt.dev", "/privacy").Body.String(), "ld+json") {
			t.Error("JSON-LD is for the home page only")
		}
		if seoGet(t, h, "rivolt.dev", "/login").Header().Get("Link") != "" {
			t.Error("no Link header on app routes")
		}
		if strings.Contains(seoGet(t, h, "preview.rivolt.dev", "/").Body.String(), "ld+json") {
			t.Error("no JSON-LD on non-public hosts")
		}
	})
	t.Run("unknown /.well-known paths are 404, not the shell", func(t *testing.T) {
		for _, p := range []string{"/.well-known/api-catalog", "/.well-known/oauth-authorization-server", "/.well-known/mcp/server-card.json", "/.well-known/http-message-signatures-directory"} {
			for _, host := range []string{"rivolt.dev", "demo.rivolt.dev", "preview.rivolt.dev"} {
				rec := seoGet(t, h, host, p)
				if rec.Code != 404 || strings.Contains(rec.Body.String(), "<html") {
					t.Errorf("%s%s: %d", host, p, rec.Code)
				}
			}
		}
	})
}
