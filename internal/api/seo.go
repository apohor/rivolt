package api

import (
	"encoding/json"
	"html"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/iosapp"
)

// Public-page metadata for crawlers and link unfurlers. The app is a
// client-rendered SPA, so every route shares one index.html; for the
// few public routes the server rewrites the head (title, description,
// canonical, Open Graph, Twitter) and adds a short <noscript> summary,
// and it answers /robots.txt and /sitemap.xml per host. Only the hosts
// below are indexable. Anything else (preview.rivolt.dev, a self-host,
// an IP) is treated as non-public: Disallow: / and X-Robots-Tag: noindex.

const (
	pathElectraFi   = "/moving-from-electrafi"
	pathSafeConnect = "/safe-to-connect"
	pathSupport     = "/support"
	pathLeaseCalc   = "/lease-calculator"
	pathChangelog   = "/changelog"
	pathChargeCost  = "/charging-cost"
	pathR2Guide     = "/r2"
	pathNetworks    = "/charging-networks"
)

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
			desc:    "The terms for using Rivolt, a free, open-source companion for Rivian owners run by ZeroDowntime LLC. " + notAffiliated,
			summary: "Rivolt terms of service. " + notAffiliated,
		},
		pathLeaseCalc: {
			title: "Rivian lease mileage calculator | Rivolt",
			desc:  "Free lease mileage calculator: on pace for your allowance, and what could overage cost? Runs in your browser. " + notAffiliated,
			summary: "A free lease mileage calculator that runs in your browser. Enter your lease dates, allowance and odometer to see miles used, whether you are on pace, " +
				"projected miles and overage at lease return, and the miles a day that keep you within the allowance. Nothing you type is sent or stored. " + notAffiliated,
		},
		// No price or date-bound figure belongs in this entry: the page shows the
		// typical fast-charge price itself, from the dated table with its 12-month
		// hide, and a copy here (title, meta, JSON-LD, sitemap, llms.txt) would
		// outlive that.
		pathChargeCost: {
			title: "How much does it cost to charge a Rivian at home? (worked example)",
			desc:  "A worked example for a Rivian R1S: home charging at your own rate, what off-peak hours change, and what one fast-charge stop adds. Free calculator, no sign-in.",
			summary: "A worked example of what home charging costs a Rivian R1S: your electricity rate, what off-peak hours change and what one fast-charge stop adds, " +
				"with a free calculator that runs in your browser. Figures on the page are examples, not measurements of any owner's truck. " + notAffiliated,
		},
		pathSafeConnect: {
			title: "Is it safe to connect my Rivian account to Rivolt?",
			desc:  "What Rivolt keeps, what it does, how to undo it, and why to connect a separate Authorized Driver account. " + notAffiliated,
			summary: "Connect a separate Authorized Driver account if you can. Rivolt keeps your Rivian email and an encrypted session token, never your password. " +
				"It is read-only and can't control your truck; sign out in Settings or remove the driver in the Rivian app to undo it. It is unofficial and can break. " + notAffiliated,
		},
		pathNetworks: {
			title: "Rivian fast-charging networks: plans and typical prices",
			desc:  "Membership plans as Electrify America, EVgo, Tesla, Shell Recharge and Rivian state them, with the date checked, and typical prices a tracker lists.",
			summary: "Fast-charging network membership plans as each operator states them, with the date we last checked, and typical observed prices per kWh where a " +
				"third-party tracker publishes them, with the month. Prices are per site and the price at the charger may differ. " + notAffiliated,
		},
		pathR2Guide: {
			title: "Your first week with an R2 in Rivolt",
			desc:  "Connecting an R2: what is recorded on the first R2s, what is unconfirmed, and how to report what looks wrong. " + notAffiliated,
			summary: "A first-week guide for Rivian R2 owners: connect an Authorized Driver account, what Rivolt records on the first R2s (drives, charges, battery, " +
				"range, location, temperatures, tire pressure, sleep state), what is not shown for an R2 yet, and how to report what looks wrong. " + notAffiliated,
		},
		pathSupport: {
			title:   "Rivolt support",
			desc:    "Help for Rivolt, the unofficial open-source companion for Rivian owners: how to connect, export or delete your data, what it does, and how to reach us.",
			summary: "Rivolt support: email support@rivolt.dev or open an issue on GitHub; answers on connecting, exporting and deleting your data. " + notAffiliated,
		},
		pathChangelog: {
			title: "What's new in Rivolt: release notes",
			desc:  "Release notes for every Rivolt version, newest first, with an Atom feed at /changelog.atom. " + notAffiliated,
			summary: "What changed in each Rivolt release, newest first: new features, fixes and behind-the-scenes changes. " +
				"Follow along with the Atom feed at https://rivolt.dev/changelog.atom. " + notAffiliated,
		},
		pathElectraFi: {
			title: "Moving your history from ElectraFi to Rivolt",
			desc:  "How to import your ElectraFi drives and charges into Rivolt, a free, open-source tracker for Rivian owners. " + notAffiliated,
			summary: "Export your ElectraFi data (Settings, Account, Advanced, Download ElectraFi Data), then upload the CSVs in Rivolt under Settings, Data, " +
				"Import ElectraFi CSV. Rivolt skips anything it has already recorded. Rivolt is not affiliated with ElectraFi. " + notAffiliated,
		},
	},
	hostDemo: {
		"/": {
			title:   "Rivolt demo: a sample Rivian truck with generated data",
			desc:    "Browse Now, Activity, Money, Plan and Truck on a sample truck with generated data. No sign-in, no Rivian account. " + notAffiliated,
			summary: "This is the Rivolt demo: a generated sample truck with made-up numbers, no sign-in and no Rivian account. " + notAffiliated,
		},
		"/privacy": {
			title:   "Privacy policy | Rivolt",
			desc:    "How Rivolt handles your data: what it records from your Rivian, how long it keeps it, and how to delete it. " + notAffiliated,
			summary: "Rivolt privacy policy. " + notAffiliated,
		},
		"/terms": {
			title:   "Terms of service | Rivolt",
			desc:    "The terms for using Rivolt, a free, open-source companion for Rivian owners run by ZeroDowntime LLC. " + notAffiliated,
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
	// Content Signals (contentsignals.org): public pages may be searched and
	// used as input for answers; training on them is not allowed.
	return "User-agent: *\nContent-Signal: search=yes, ai-input=yes, ai-train=no\nDisallow: /api/\n\nSitemap: https://" + host + "/sitemap.xml\n"
}

const repoURL = "https://github.com/apohor/rivolt"

// llmsTxt is the reading list for LLM tools (llmstxt.org): public pages only.
func llmsTxt(host string) (string, bool) {
	pages, ok := seoPages[host]
	if !ok {
		return "", false
	}
	home := pages["/"]
	base := "https://" + host
	var b strings.Builder
	b.WriteString("# Rivolt\n\n> " + home.desc + "\n\n")
	b.WriteString("Rivolt records a Rivian's drives and charges, prices charging at the owner's own rate, shows efficiency, and projects lease miles. ")
	b.WriteString("It is free, open source (MIT) and run by ZeroDowntime LLC.\n\n## Pages\n\n")
	b.WriteString("- [Home](" + base + "/): what Rivolt does\n")
	if host == hostApp {
		b.WriteString("- [Demo](https://" + hostDemo + "/): a generated sample truck, no sign-in\n")
	}
	b.WriteString("- [Privacy policy](" + base + "/privacy): what is recorded, retention and deletion\n")
	b.WriteString("- [Terms of service](" + base + "/terms)\n")
	if _, ok := pages[pathSupport]; ok {
		b.WriteString("- [Support](" + base + pathSupport + "): how to get help\n")
	}
	if _, ok := pages[pathLeaseCalc]; ok {
		b.WriteString("- [Rivian lease mileage calculator](" + base + pathLeaseCalc + "): runs in your browser\n")
	}
	if _, ok := pages[pathChargeCost]; ok {
		b.WriteString("- [How much does it cost to charge a Rivian at home?](" + base + pathChargeCost + "): a worked example and a free calculator\n")
	}
	if _, ok := pages[pathNetworks]; ok {
		b.WriteString("- [Fast-charging networks: plans and typical prices](" + base + pathNetworks + "): plans as each operator states them, with the date checked\n")
	}
	if _, ok := pages[pathR2Guide]; ok {
		b.WriteString("- [Your first week with an R2](" + base + pathR2Guide + "): what is recorded on the first R2s and what is not yet\n")
	}
	if _, ok := pages[pathSafeConnect]; ok {
		b.WriteString("- [Is it safe to connect my Rivian account?](" + base + pathSafeConnect + "): what is kept and how to undo it\n")
	}
	if _, ok := pages[pathElectraFi]; ok {
		b.WriteString("- [Moving from ElectraFi](" + base + pathElectraFi + "): import ElectraFi history\n")
	}
	if _, ok := pages[pathChangelog]; ok {
		b.WriteString("- [What's new](" + base + pathChangelog + "): release notes, with an Atom feed at " + base + "/changelog.atom\n")
	}
	if _, ok := pages[pathSampleTrips]; ok {
		b.WriteString("- [Sample road trips](" + base + pathSampleTrips + "): routes planned with charging stops and estimated costs\n")
		for _, p := range sampleTripPaths()[1:] {
			b.WriteString("  - [" + pages[p].title + "](" + base + p + ")\n")
		}
	}
	b.WriteString("\n## Source\n\n")
	b.WriteString("- [GitHub](" + repoURL + "): source code and self-hosting\n")
	b.WriteString("- [Sign-up and import guide](" + repoURL + "/blob/main/docs/SIGNUP.md)\n\n")
	b.WriteString("Rivolt is unofficial and not affiliated with, endorsed by or sponsored by Rivian. Rivian is a trademark of Rivian Automotive, LLC.\n")
	return b.String(), true
}

// linkHeader advertises machine-readable resources (RFC 8288).
func linkHeader(canon string) string {
	l := `</sitemap.xml>; rel="sitemap", </llms.txt>; rel="describedby"; type="text/plain"`
	if canon == "/" {
		l += `, </privacy>; rel="privacy-policy", </terms>; rel="terms-of-service"`
	}
	return l
}

// wantsMarkdown reports whether the Accept header asks for text/markdown at
// least as strongly as text/html. Browsers never send text/markdown.
func wantsMarkdown(accept string) bool {
	md, ht := -1.0, -1.0
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		typ := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				if v, err := strconv.ParseFloat(f[2:], 64); err == nil {
					q = v
				}
			}
		}
		switch typ {
		case "text/markdown":
			md = q
		case "text/html":
			ht = q
		}
	}
	return md > 0 && md >= ht
}

// markdownPage is a short Markdown rendering of a public page. The full
// privacy and terms text stay on their HTML pages (linked).
func markdownPage(host string, page seoPage, canon string) string {
	url := "https://" + host + canon
	var b strings.Builder
	b.WriteString("---\ntitle: " + strconv.Quote(page.title) + "\ndescription: " + strconv.Quote(page.desc) + "\nurl: " + url + "\n---\n\n")
	b.WriteString("# " + page.title + "\n\n" + page.summary + "\n\n")
	switch canon {
	case "/":
		b.WriteString("- [Privacy policy](https://" + host + "/privacy)\n- [Terms of service](https://" + host + "/terms)\n")
		if host == hostApp {
			b.WriteString("- [Demo with a sample truck](https://" + hostDemo + "/)\n")
		}
		b.WriteString("- [Source on GitHub](" + repoURL + ")\n- [Reading list for LLMs](https://" + host + "/llms.txt)\n")
	default:
		b.WriteString("Full text: " + url + "\n")
	}
	return b.String()
}

// jsonLD is schema.org data for the home page: a free web application and
// its site. No ratings, reviews or offers beyond price 0. A non-executed
// data block, so the script-src CSP does not apply to it.
func jsonLD(host string) string {
	url := "https://" + host + "/"
	home := seoPages[host]["/"]
	doc := map[string]any{
		"@context": "https://schema.org",
		"@graph": []any{
			map[string]any{"@type": "WebSite", "@id": url + "#website", "url": url, "name": "Rivolt"},
			map[string]any{
				"@type": "SoftwareApplication", "@id": url + "#app", "name": "Rivolt", "url": url,
				"description":         home.desc,
				"applicationCategory": "UtilitiesApplication",
				"operatingSystem":     "Web",
				"isAccessibleForFree": true,
				"license":             "https://opensource.org/licenses/MIT",
				"offers":              map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "USD"},
				"sameAs":              []string{repoURL},
			},
		},
	}
	b, _ := json.Marshal(doc) // escapes <, > and & as \u003c etc.
	return `<script type="application/ld+json">` + string(b) + `</script>`
}

// seoNow is the clock the served pages read; tests set it.
var seoNow = time.Now

// chargingCostPrice is the data element the /charging-cost page reads for its
// fast-charge example: the typical price DCFC Tracker reports for the Rivian
// Adventure Network (USD per kWh) and the month it was seen, built at request time
// from the dated table so it is gone once that is 12 months old (dcfcrates.ObservedFor).
// It is empty when there is no figure, and never part of the summary, the JSON-LD,
// llms.txt or the markdown view: only this element, only on the app host, so a cached
// copy of those can never outlive the figure. A non-executed data block, so the
// script-src CSP does not apply; json.Marshal escapes <, > and &.
func chargingCostPrice(host string, now time.Time) string {
	if host != hostApp {
		return ""
	}
	o, ok := dcfcrates.ObservedFor("ran", now)
	if !ok {
		return ""
	}
	b, err := json.Marshal(map[string]any{"fast_price": o.Low, "price_month": o.Month})
	if err != nil {
		return ""
	}
	return `<script type="application/json" id="charging-cost">` + string(b) + `</script>`
}

// articleLD is schema.org Article data for a public guide page: headline,
// description and publisher only. Nothing price- or date-bound.
func articleLD(host string, page seoPage, canon string) string {
	url := "https://" + host + canon
	doc := map[string]any{
		"@context": "https://schema.org",
		"@graph": []any{
			map[string]any{"@type": "Article", "@id": url + "#article", "headline": page.title, "description": page.desc, "url": url,
				"publisher": map[string]any{"@type": "Organization", "name": "Rivolt", "url": "https://" + host + "/"}},
		},
	}
	b, _ := json.Marshal(doc) // escapes <, > and & as \u003c etc.
	return `<script type="application/ld+json">` + string(b) + `</script>`
}

func sitemapXML(host string) (string, bool) {
	pages, ok := seoPages[host]
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range append([]string{"/", "/privacy", "/terms", pathSupport, pathLeaseCalc, pathChargeCost, pathNetworks, pathR2Guide, pathSafeConnect, pathElectraFi, pathChangelog}, sampleTripPaths()...) {
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
	extra := ""
	switch canon {
	case "/":
		extra = "    " + jsonLD(host) + "\n"
	case pathChargeCost:
		extra = "    " + articleLD(host, page, canon) + "\n"
		if el := chargingCostPrice(host, seoNow()); el != "" {
			extra += "    " + el + "\n"
		}
	}
	// Smart App Banner: only for an App Store link with an app id (the server must emit it;
	// a tag added by script is unreliable on Safari). TestFlight links get none.
	if id := iosapp.AppStoreID(iosapp.URL()); id != "" {
		extra += `    <meta name="apple-itunes-app" content="app-id=` + e(id) + `" />` + "\n"
	}
	rep(reHeadClose, `<link rel="canonical" href="`+e(url)+`" />`+"\n"+
		`    <meta property="og:url" content="`+e(url)+`" />`+"\n"+extra+"  </head>")
	rep(reRootDiv, `<div id="root"></div>`+"\n    <noscript><h1>Rivolt</h1><p>"+e(page.summary)+"</p></noscript>")
	return []byte(out)
}
