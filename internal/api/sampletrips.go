package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/demo"
)

// Public sample-trip pages on rivolt.dev (Sprint 4; design
// sprint4-sample-trip-pages.md): /sample-trips and one page per preset in
// demo.SampleTripPages. Each is rendered once per process from the embedded
// presets and index.html; a request only copies bytes (no database, planner,
// geocoder or tile call). The body carries a semantic <article> for crawlers
// and no-JS readers inside #root, which the app replaces on mount, and the
// page model as inline JSON for the SampleTripPage component.

const pathSampleTrips = "/sample-trips"

func init() {
	pages := seoPages[hostApp]
	pages[pathSampleTrips] = seoPage{title: demo.SampleTripHub.Title, desc: demo.SampleTripHub.Desc,
		summary: demo.SampleTripHub.Intro + " " + notAffiliated}
	for _, p := range demo.SampleTripPages {
		pages[pathSampleTrips+"/"+p.Slug] = seoPage{title: p.Title, desc: p.Desc, summary: p.Intro + " " + notAffiliated}
	}
}

// sampleTripPaths is the hub then each page, in hub order.
func sampleTripPaths() []string {
	out := []string{pathSampleTrips}
	for _, p := range demo.SampleTripPages {
		out = append(out, pathSampleTrips+"/"+p.Slug)
	}
	return out
}

// isSampleTripPath reports whether path is under /sample-trips/ (a page slug,
// known or not).
func isSampleTripPath(path string) bool {
	return strings.HasPrefix(path, pathSampleTrips+"/") && len(path) > len(pathSampleTrips)+1
}

type renderedPage struct {
	html, markdown []byte
	etag           string
}

var (
	sampleTripMu    sync.Mutex
	sampleTripCache = map[string]renderedPage{}
)

// sampleTripPage returns the rendered page for canon ("/sample-trips" or
// "/sample-trips/<slug>"), building it on first use from the shell.
func sampleTripPage(shell []byte, canon string) (renderedPage, bool) {
	sampleTripMu.Lock()
	defer sampleTripMu.Unlock()
	if p, ok := sampleTripCache[canon]; ok {
		return p, true
	}
	page, _, ok := seoLookup(hostApp, canon)
	if !ok {
		return renderedPage{}, false
	}
	var article, data, ld bytes.Buffer
	var md string
	if canon == pathSampleTrips {
		var trips []demo.SampleTrip
		for _, p := range demo.SampleTripPages {
			if t, ok := demo.SampleTripBySlug(p.Slug); ok {
				trips = append(trips, t)
			}
		}
		if err := hubTemplate.Execute(&article, hubView{Hub: demo.SampleTripHub, Pages: demo.SampleTripPages, NotAffiliated: notAffiliated}); err != nil {
			return renderedPage{}, false
		}
		type hubTrip struct {
			Slug   string               `json:"slug"`
			Title  string               `json:"title"`
			Intro  string               `json:"intro"`
			Blurb  string               `json:"blurb"`
			Totals demo.SampleTripTotal `json:"totals"`
		}
		list := make([]hubTrip, 0, len(trips))
		for i, t := range trips {
			list = append(list, hubTrip{t.Slug, t.Title, t.Intro, demo.SampleTripPages[i].Blurb, t.Totals})
		}
		writeJSONScript(&data, "sample-trips", map[string]any{"trips": list})
		ld.WriteString(breadcrumbLD([][2]string{{"Rivolt", "/"}, {"Sample trips", pathSampleTrips}}, page))
		md = hubMarkdown(trips)
	} else {
		trip, ok := demo.SampleTripBySlug(strings.TrimPrefix(canon, pathSampleTrips+"/"))
		if !ok {
			return renderedPage{}, false
		}
		if err := tripTemplate.Execute(&article, tripView{Trip: trip, Map: routeSVG(trip), NotAffiliated: notAffiliated}); err != nil {
			return renderedPage{}, false
		}
		writeJSONScript(&data, "sample-trip", trip)
		ld.WriteString(breadcrumbLD([][2]string{{"Rivolt", "/"}, {"Sample trips", pathSampleTrips}, {trip.Title, canon}}, page))
		md = tripMarkdown(trip, canon)
	}
	out := injectSEO(shell, hostApp, page, canon)
	out = bytes.Replace(out, []byte("</head>"), []byte("  "+ld.String()+"\n  </head>"), 1)
	out = bytes.Replace(out, []byte(`<div id="root"></div>`),
		[]byte(`<div id="root">`+article.String()+`</div>`+"\n    "+data.String()), 1)
	sum := sha256.Sum256(out)
	p := renderedPage{html: out, markdown: []byte(md), etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	sampleTripCache[canon] = p
	return p, true
}

// serveSampleTrip writes a rendered page with its ETag, honouring
// If-None-Match and the Markdown variant. The HTML references this build's
// hashed bundle, so it is revalidated every time (a cheap 304), never held
// past a deploy.
func serveSampleTrip(w http.ResponseWriter, r *http.Request, p renderedPage, canon string) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Add("Vary", "Accept")
	h.Set("Link", linkHeader(canon))
	body, ctype, etag := p.html, "text/html; charset=utf-8", p.etag
	if wantsMarkdown(r.Header.Get("Accept")) {
		body, ctype, etag = p.markdown, "text/markdown; charset=utf-8", strings.TrimSuffix(p.etag, `"`)+`-md"`
	}
	h.Set("ETag", etag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", ctype)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func writeJSONScript(b *bytes.Buffer, id string, v any) {
	j, _ := json.Marshal(v) // escapes <, > and &, so it can't close the script
	b.WriteString(`<script type="application/json" id="` + id + `">`)
	b.Write(j)
	b.WriteString(`</script>`)
}

// breadcrumbLD is an Article plus its BreadcrumbList. No ratings or reviews.
func breadcrumbLD(crumbs [][2]string, page seoPage) string {
	items := make([]any, 0, len(crumbs))
	for i, c := range crumbs {
		items = append(items, map[string]any{"@type": "ListItem", "position": i + 1, "name": c[0], "item": "https://" + hostApp + c[1]})
	}
	url := "https://" + hostApp + crumbs[len(crumbs)-1][1]
	doc := map[string]any{
		"@context": "https://schema.org",
		"@graph": []any{
			map[string]any{"@type": "Article", "@id": url + "#article", "headline": page.title, "description": page.desc,
				"url": url, "publisher": map[string]any{"@type": "Organization", "name": "Rivolt", "url": "https://" + hostApp + "/"}},
			map[string]any{"@type": "BreadcrumbList", "itemListElement": items},
		},
	}
	j, _ := json.Marshal(doc)
	return `<script type="application/ld+json">` + string(j) + `</script>`
}

type hubView struct {
	Hub           demo.SampleTripPage
	Pages         []demo.SampleTripPage
	NotAffiliated string
}

type tripView struct {
	Trip          demo.SampleTrip
	Map           template.HTML
	NotAffiliated string
}

var tmplFuncs = template.FuncMap{
	"mi":  func(f float64) string { return fmt.Sprintf("%.0f mi", f) },
	"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f) },
	"usd": func(f float64) string { return fmt.Sprintf("$%.2f", f) },
	// stopCost is one stop's estimate, or where its price is found.
	"stopCost": stopCostText,
	"costLine": tripCostLine,
	"kw":       func(f float64) string { return fmt.Sprintf("%.0f kW", f) },
	"kwh":      func(f float64) string { return fmt.Sprintf("%.1f kWh", f) },
	"hm":       durationHM,
	// Times of day only: the presets have fixed dates, which would read as
	// stale on an evergreen page.
	"clock": func(t time.Time, tz string) string {
		return t.In(demo.ZoneOf(demo.SampleTrip{TZ: tz})).Format("3:04 PM")
	},
	"h1":   func(title string) string { return strings.TrimSuffix(title, " | Rivolt") },
	"path": func(slug string) string { return pathSampleTrips + "/" + slug },
}

// stopCostText words a stop's cost: a typical observed price as a dollar figure
// (a range where the tracker reports a membership and a non-member price) with its
// source, or "price shown at the charger" when the network has no figure.
func stopCostText(s demo.SampleTripStop) string {
	if s.CostLow == nil || s.CostHigh == nil {
		return "price shown at the charger"
	}
	return dollarRange(*s.CostLow, *s.CostHigh)
}

func dollarRange(low, high float64) string {
	if high-low < 0.005 {
		return fmt.Sprintf("$%.2f", low)
	}
	return fmt.Sprintf("$%.2f to $%.2f", low, high)
}

// tripCostLine is the sentence about fast-charging cost. It is an estimate from the
// third-party typical price, never a listed price, and it says how many stops it
// could not price.
func tripCostLine(t demo.SampleTripTotal) string {
	// "Oct 2026": the month is the honest part of the claim.
	seen := "typical price seen by DCFC Tracker"
	if m, err := time.Parse("2006-01", t.PriceMonth); err == nil {
		seen += ", " + m.Format("Jan 2006")
	}
	switch {
	case t.PricedStops == 0:
		return "The price at the fast chargers is shown at each charger."
	case t.UnpricedStops == 0:
		return fmt.Sprintf("About %s fast charging (estimate, %s; the price at the charger may differ).", dollarRange(t.DCFCCostLow, t.DCFCCostHigh), seen)
	default:
		return fmt.Sprintf("About %s for %d of %d fast-charging stops (estimate, %s); the price at the other %d is shown at the charger.",
			dollarRange(t.DCFCCostLow, t.DCFCCostHigh), t.PricedStops, t.ChargeStops, seen, t.UnpricedStops)
	}
}

func durationHM(min float64) string {
	m := int(math.Round(min))
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

var hubTemplate = template.Must(template.New("hub").Funcs(tmplFuncs).Parse(`<article id="sample-trips-article">
<h1>{{h1 .Hub.Title}}</h1>
<p>{{.Hub.Intro}}</p>
<ul>{{range .Pages}}
<li><a href="{{path .Slug}}">{{.Title}}</a>. {{.Blurb}}</li>{{end}}
</ul>
<p><a href="/signup">Plan your own trip</a></p>
<p>{{.NotAffiliated}}</p>
</article>`))

var tripTemplate = template.Must(template.New("trip").Funcs(tmplFuncs).Parse(`<article id="sample-trip-article">
<p><a href="/sample-trips">Sample trips</a></p>
<h1>{{h1 .Trip.Title}}</h1>
<p>{{.Trip.Intro}}</p>
{{.Map}}
<p>{{mi .Trip.Totals.DistanceMi}}, {{hm .Trip.Totals.DriveMin}} driving, {{.Trip.Totals.ChargeStops}} charging stop{{if ne .Trip.Totals.ChargeStops 1}}s{{end}} ({{hm .Trip.Totals.ChargeMin}}). {{costLine .Trip.Totals}}</p>
{{range .Trip.Days}}
<section>
{{if .Label}}<h2>{{.Label}}: {{.From}} to {{.To}}</h2>{{else}}<h2>{{.From}} to {{.To}}</h2>{{end}}
<table>
<thead><tr><th>Stop</th><th>Network</th><th>Arrive</th><th>Leave</th><th>Charging</th><th>Est. cost</th></tr></thead>
<tbody>
<tr><td>Depart {{.From}}</td><td></td><td></td><td>{{clock .DepartAt $.Trip.TZ}}</td><td></td><td></td></tr>
{{range .Stops}}<tr><td>{{.Name}}</td><td>{{.Network}}</td><td>{{pct .ArriveSoC}}</td><td>{{pct .DepartSoC}}</td><td>{{hm .ChargeMin}}{{if .MaxKW}} (up to {{kw .MaxKW}}){{end}}</td><td>{{stopCost .}}</td></tr>
{{end}}<tr><td>Arrive {{.To}}</td><td></td><td>{{pct .ArrivalSoC}}</td><td>{{clock .ArriveAt $.Trip.TZ}}</td><td></td><td></td></tr>
{{with .Overnight}}<tr><td>Hotel stay at {{.Name}}, {{.Nights}} night{{if ne .Nights 1}}s{{end}}</td><td>Level 2</td><td></td><td>{{pct .SoCAfter}}</td><td>{{printf "%.0f h" .ParkedHours}} parked, {{kw .L2KW}}</td><td>adds {{kwh .AddedKWh}}</td></tr>
{{end}}</tbody>
</table>
<p>{{mi .DistanceMi}}, {{hm .DriveMin}} driving.</p>
</section>
{{end}}
<p><a href="{{.Trip.DemoURL}}">Open this trip in the demo planner</a> · <a href="{{.Trip.SignupURL}}">Plan your own</a></p>
<p>Costs are estimates at each network's guest rate; your membership or home rate changes them. {{.NotAffiliated}}</p>
</article>`))

// routeSVG draws the route and its stops as inline SVG: no tiles, no script.
func routeSVG(t demo.SampleTrip) template.HTML {
	if len(t.Route) < 2 {
		return "" // no shape, no map (the demo package refuses such a preset anyway)
	}
	const w, h, pad = 640.0, 360.0, 16.0
	minLat, maxLat, minLon, maxLon := 90.0, -90.0, 180.0, -180.0
	for _, p := range t.Route {
		minLat, maxLat = math.Min(minLat, p[0]), math.Max(maxLat, p[0])
		minLon, maxLon = math.Min(minLon, p[1]), math.Max(maxLon, p[1])
	}
	k := math.Cos((minLat + maxLat) / 2 * math.Pi / 180) // longitude shrinks with latitude
	spanX, spanY := (maxLon-minLon)*k, maxLat-minLat
	scale := math.Min((w-2*pad)/math.Max(spanX, 1e-6), (h-2*pad)/math.Max(spanY, 1e-6))
	xy := func(lat, lon float64) (float64, float64) {
		return pad + (lon-minLon)*k*scale, h - pad - (lat-minLat)*scale
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg role="img" aria-label="Route map: %s" viewBox="0 0 %.0f %.0f" width="100%%" style="max-width:640px;height:auto"><polyline fill="none" stroke="#2563eb" stroke-width="3" stroke-linejoin="round" points="`,
		template.HTMLEscapeString(t.Title), w, h)
	for i, p := range t.Route {
		x, y := xy(p[0], p[1])
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	b.WriteString(`"/>`)
	first, last := t.Route[0], t.Route[len(t.Route)-1]
	for _, p := range [][2]float64{first, last} {
		x, y := xy(p[0], p[1])
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="6" fill="#111827"/>`, x, y)
	}
	// Charging stops.
	for _, d := range t.Days {
		for _, s := range d.Stops {
			if s.Lat == 0 && s.Lon == 0 {
				continue
			}
			x, y := xy(s.Lat, s.Lon)
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="5" fill="#16a34a" stroke="#fff" stroke-width="2"/>`, x, y)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) // built from numbers and an escaped title only
}

func tripMarkdown(t demo.SampleTrip, canon string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %q\nurl: https://%s%s\n---\n\n# %s\n\n%s\n\n", t.Title, hostApp, canon, strings.TrimSuffix(t.Title, " | Rivolt"), t.Intro)
	fmt.Fprintf(&b, "%s, %s driving, %d charging stop(s) (%s). %s\n\n",
		tmplFuncs["mi"].(func(float64) string)(t.Totals.DistanceMi), durationHM(t.Totals.DriveMin), t.Totals.ChargeStops,
		durationHM(t.Totals.ChargeMin), tripCostLine(t.Totals))
	for _, d := range t.Days {
		title := d.From + " to " + d.To
		if d.Label != "" {
			title = d.Label + ": " + title
		}
		fmt.Fprintf(&b, "## %s\n\n| Stop | Network | Arrive | Leave | Charging | Est. cost |\n|---|---|---|---|---|---|\n", title)
		for _, s := range d.Stops {
			fmt.Fprintf(&b, "| %s | %s | %.0f%% | %.0f%% | %s | %s |\n", s.Name, s.Network, s.ArriveSoC, s.DepartSoC, durationHM(s.ChargeMin), stopCostText(s))
		}
		if o := d.Overnight; o != nil {
			fmt.Fprintf(&b, "| Hotel stay at %s, %d night(s) | Level 2 | | %.0f%% | %.0f h parked, %.0f kW | adds %.1f kWh |\n", o.Name, o.Nights, o.SoCAfter, o.ParkedHours, o.L2KW, o.AddedKWh)
		}
		fmt.Fprintf(&b, "\n%.0f mi, %s driving, arriving at %.0f%%.\n\n", d.DistanceMi, durationHM(d.DriveMin), d.ArrivalSoC)
	}
	fmt.Fprintf(&b, "- [Open in the demo planner](%s)\n- [Plan your own](https://%s/signup)\n\n%s\n", t.DemoURL, hostApp, notAffiliated)
	return b.String()
}

func hubMarkdown(trips []demo.SampleTrip) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %q\nurl: https://%s%s\n---\n\n# %s\n\n%s\n\n", demo.SampleTripHub.Title, hostApp, pathSampleTrips, strings.TrimSuffix(demo.SampleTripHub.Title, " | Rivolt"), demo.SampleTripHub.Intro)
	for _, t := range trips {
		fmt.Fprintf(&b, "- [%s](https://%s%s/%s): %.0f mi, %d charging stop(s)\n",
			t.Title, hostApp, pathSampleTrips, t.Slug, t.Totals.DistanceMi, t.Totals.ChargeStops)
	}
	fmt.Fprintf(&b, "\n%s\n", notAffiliated)
	return b.String()
}
