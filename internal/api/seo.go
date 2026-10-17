package api

import (
	"html"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// Public-page metadata for crawlers and link unfurlers. The app is a
// client-rendered SPA, so every route shares one index.html; for the
// few public routes the server rewrites the head (title, description,
// canonical, Open Graph, Twitter) and adds a short <noscript> summary,
// and it answers /robots.txt and /sitemap.xml per host. Only the hosts
// below are indexable. Anything else (preview.rivolt.dev, a self-host,
// an IP) is treated as non-public: Disallow: / and X-Robots-Tag: noindex.

const (
	hostApp  = "rivolt.dev"
	hostDemo = "demo.rivolt.dev"
)

type seoPage struct {
	title, desc, summary string
}

const notAffiliated = "Unofficial, not affiliated with Rivian."

var seoPages = map[string]map[string]seoPage{
	hostApp: {
		"/": {
			title: "Rivolt: Rivian charging cost, trip and efficiency tracker",
			desc:  "Free, open-source tracker for Rivian owners: charging cost, trips, efficiency and lease miles. " + notAffiliated,
			summary: "Rivolt is a free, open-source tracker for Rivian owners. It records drives and charges, prices charging at your own rate, " +
				"shows efficiency, and projects lease miles. Try the sample truck at https://demo.rivolt.dev. " + notAffiliated,
		},
		"/privacy": {
			title:   "Privacy policy | Rivolt",
			desc:    "How Rivolt handles your data: what it records from your Rivian, how long it keeps it, and how to delete it. " + notAffiliated,
			summary: "Rivolt privacy policy. " + notAffiliated,
		},
		"/terms": {
			title:   "Terms of service | Rivolt",
			desc:    "The terms for using Rivolt, a free, open-source companion for Rivian owners run by an individual. " + notAffiliated,
			summary: "Rivolt terms of service. " + notAffiliated,
		},
	},
	hostDemo: {
		"/": {
			title: "Rivolt demo: a sample Rivian truck with generated data",
			desc: "Browse Rivolt's Now, Activity, Money, Plan and Truck tabs on a generated sample truck. No sign-in, no Rivian account, sample data only. " +
				notAffiliated,
			summary: "This is the Rivolt demo: a generated sample truck with made-up numbers, no sign-in and no Rivian account. " + notAffiliated,
		},
		"/privacy": {
			title:   "Privacy policy | Rivolt",
			desc:    "How Rivolt handles your data: what it records from your Rivian, how long it keeps it, and how to delete it. " + notAffiliated,
			summary: "Rivolt privacy policy. " + notAffiliated,
		},
		"/terms": {
			title:   "Terms of service | Rivolt",
			desc:    "The terms for using Rivolt, a free, open-source companion for Rivian owners run by an individual. " + notAffiliated,
			summary: "Rivolt terms of service. " + notAffiliated,
		},
	},
}

// requestHost returns the lower-cased Host without a port.
func requestHost(r *http.Request) string {
	h := strings.ToLower(r.Host)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(h, ".")
}

// seoLookup returns the page metadata for host+path, and the canonical
// path. ok is false when the route is not a public page on an indexable
// host. On the demo host /demo is the same page as /.
func seoLookup(host, path string) (page seoPage, canon string, ok bool) {
	pages, found := seoPages[host]
	if !found {
		return seoPage{}, "", false
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	if host == hostDemo && path == "/demo" {
		path = "/"
	}
	page, ok = pages[path]
	return page, path, ok
}

func robotsTxt(host string) string {
	if _, ok := seoPages[host]; !ok {
		return "User-agent: *\nDisallow: /\n"
	}
	return "User-agent: *\nDisallow: /api/\n\nSitemap: https://" + host + "/sitemap.xml\n"
}

func sitemapXML(host string) (string, bool) {
	pages, ok := seoPages[host]
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range []string{"/", "/privacy", "/terms"} {
		if _, ok := pages[p]; ok {
			b.WriteString("  <url><loc>https://" + host + p + "</loc></url>\n")
		}
	}
	b.WriteString("</urlset>\n")
	return b.String(), true
}

var (
	reTitle     = regexp.MustCompile(`(?s)<title>.*?</title>`)
	reDesc      = regexp.MustCompile(`(?s)<meta\s+name="description"\s+content="[^"]*"\s*/?>`)
	reOGTitle   = regexp.MustCompile(`(?s)<meta\s+property="og:title"\s+content="[^"]*"\s*/?>`)
	reOGDesc    = regexp.MustCompile(`(?s)<meta\s+property="og:description"\s+content="[^"]*"\s*/?>`)
	reTwTitle   = regexp.MustCompile(`(?s)<meta\s+name="twitter:title"\s+content="[^"]*"\s*/?>`)
	reTwDesc    = regexp.MustCompile(`(?s)<meta\s+name="twitter:description"\s+content="[^"]*"\s*/?>`)
	reRootDiv   = regexp.MustCompile(`<div id="root"></div>`)
	reHeadClose = regexp.MustCompile(`</head>`)
)

// injectSEO rewrites the head of the shell for one public page. Each
// replacement is skipped when its anchor is missing, so a changed
// index.html degrades to the generic tags instead of breaking.
func injectSEO(shell []byte, host string, page seoPage, canon string) []byte {
	url := "https://" + host + canon
	e := html.EscapeString
	out := string(shell)
	rep := func(re *regexp.Regexp, with string) {
		out = re.ReplaceAllLiteralString(out, with)
	}
	rep(reTitle, "<title>"+e(page.title)+"</title>")
	rep(reDesc, `<meta name="description" content="`+e(page.desc)+`" />`)
	rep(reOGTitle, `<meta property="og:title" content="`+e(page.title)+`" />`)
	rep(reOGDesc, `<meta property="og:description" content="`+e(page.desc)+`" />`)
	rep(reTwTitle, `<meta name="twitter:title" content="`+e(page.title)+`" />`)
	rep(reTwDesc, `<meta name="twitter:description" content="`+e(page.desc)+`" />`)
	rep(reHeadClose, `<link rel="canonical" href="`+e(url)+`" />`+"\n"+
		`    <meta property="og:url" content="`+e(url)+`" />`+"\n  </head>")
	rep(reRootDiv, `<div id="root"></div>`+"\n    <noscript><h1>Rivolt</h1><p>"+e(page.summary)+"</p></noscript>")
	return []byte(out)
}
