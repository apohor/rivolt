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

// Public sample-trip pages (rivolt.dev/sample-trips and /sample-trips/<slug>).
//
// Each page is built once, from the embedded demo presets, into a semantic
// <article> with an inline SVG map, and served to crawlers and no-JS readers
// inside #root; the SPA's SampleTripPage replaces it on mount from the inline
// JSON. A request only copies pre-rendered bytes: no database read, planner,
// router, geocoder, weather, LLM or Rivian call, and the query string is ignored.
// Only the app host serves them (the demo host stays noindex).

const (
	pathSampleTrips    = "/sample-trips"
	sampleTripCache    = "public, max-age=3600, stale-while-revalidate=86400"
	sampleTripZone     = "America/Los_Angeles"
	demoPlannerURL     = "https://demo.rivolt.dev/trips/plan?preset="
	signupPath         = "/signup"
	sampleTripMapW     = 640
	sampleTripMapH     = 360
	sampleTripMapPad   = 24
	metersPerMile      = 1609.344
	articleDOMClass    = "sample-trip"
	articleHubDOMClass = "sample-trips-hub"
)

// sampleStop is one row of a day's table.
type sampleStop struct {
	Kind        string // origin, charge, hotel, destination
	Name        string
	Network     string
	ArriveClock string
	ArriveSoC   string
	Charge      string
	DepartClock string
	DepartSoC   string
	Estimate    string
}

type sampleDay struct {
	Title    string
	Summary  string
	Stops    []sampleStop
	Overnite string
}

// samplePage is a page ready to serve (apart from the app shell).
type samplePage struct {
	Slug        string
	Path        string
	Title       string // full <title>
	Heading     string // h1: the title without the site suffix
	Description string
	Intro       string
	Days        []sampleDay
	Totals      string
	MapSVG      template.HTML
	Article     template.HTML
	JSON        []byte // inline #sample-trip document
	LD          string // JSON-LD <script>
	Markdown    string
}

type sampleSite struct {
	hub   *samplePage
	pages []*samplePage
	bySlg map[string]*samplePage
	err   error
}

var (
	sampleSiteOnce sync.Once
	sampleSiteVal  *sampleSite
)

// sampleTripSite builds every page once. An error (a malformed table or preset)
// leaves the site empty: the paths then fall back to the app's own 404.
func sampleTripSite() *sampleSite {
	sampleSiteOnce.Do(func() { sampleSiteVal = buildSampleSite() })
	return sampleSiteVal
}

func trimSiteSuffix(s string) string { return strings.TrimSuffix(s, " | Rivolt") }

func buildSampleSite() *sampleSite {
	site := &sampleSite{bySlg: map[string]*samplePage{}}
	set, err := demo.SampleTrips()
	if err != nil {
		site.err = err
		return site
	}
	for _, c := range set.Trips {
		p, err := buildSamplePage(c)
		if err != nil {
			site.err = fmt.Errorf("sample trip %s: %w", c.Slug, err)
			return &sampleSite{bySlg: map[string]*samplePage{}, err: site.err}
		}
		site.pages = append(site.pages, p)
		site.bySlg[c.Slug] = p
	}
	site.hub = buildSampleHub(set, site.pages)
	return site
}

// ---- reading a preset ----------------------------------------------------

type planWP struct {
	WaypointType      string  `json:"WaypointType"`
	Name              string  `json:"Name"`
	Latitude          float64 `json:"Latitude"`
	Longitude         float64 `json:"Longitude"`
	MaxPowerKW        float64 `json:"MaxPowerKW"`
	ChargeDurationSec float64 `json:"ChargeDurationSec"`
	ArrivalSoC        float64 `json:"ArrivalSoC"`
	DepartureSoC      float64 `json:"DepartureSoC"`
	ArrivalTimeUTC    string  `json:"ArrivalTimeUTC"`
	DepartureTimeUTC  string  `json:"DepartureTimeUTC"`
}

type planRoute struct {
	Waypoints                []planWP `json:"Waypoints"`
	TotalDriveDistanceMeters float64  `json:"TotalDriveDistanceMeters"`
	TotalDriveDurationSec    float64  `json:"TotalDriveDurationSec"`
	TotalChargingDurationSec float64  `json:"TotalChargingDurationSec"`
	TotalTripDurationSec     float64  `json:"TotalTripDurationSec"`
	ArrivalSoC               float64  `json:"ArrivalSoC"`
}

type planBreakdown struct {
	NetworkName string  `json:"network_name"`
	ChargerName string  `json:"charger_name"`
	EnergyKWh   float64 `json:"energy_kwh"`
	UserRate    float64 `json:"user_rate"`
	MemberPlan  string  `json:"member_plan"`
}

type planCost struct {
	Currency            string          `json:"currency"`
	DcfcSpendUserMember float64         `json:"dcfc_spend_user_member"`
	HomeEquivalent      float64         `json:"home_equivalent"`
	Breakdown           []planBreakdown `json:"breakdown"`
}

// planDoc is a single-route preset ("Routes"/"costs") or a multi-day one ("days").
type planDoc struct {
	Routes []planRoute `json:"Routes"`
	Costs  []planCost  `json:"costs"`
	Kind   string      `json:"kind"`
	Days   []struct {
		Plan struct {
			Routes []planRoute `json:"Routes"`
		} `json:"plan"`
		Costs     []planCost `json:"costs"`
		Overnight *struct {
			Name        string  `json:"name"`
			ParkedHours float64 `json:"parked_hours"`
			L2KW        float64 `json:"l2_kw"`
			AddedKWh    float64 `json:"added_kwh"`
			PostSoC     float64 `json:"post_charge_soc_pct"`
		} `json:"overnight"`
	} `json:"days"`
	Total struct {
		DistanceMeters   float64 `json:"distance_meters"`
		DriveDurationSec float64 `json:"drive_duration_sec"`
		ChargingSec      float64 `json:"charging_duration_sec"`
	} `json:"total"`
}

type presetMeta struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Inputs        json.RawMessage `json:"inputs"`
	Summary       json.RawMessage `json:"summary"`
	RoutePolyline string          `json:"route_polyline"`
}

func presetMetaByID(id string) (presetMeta, bool) {
	var idx struct {
		Trips []presetMeta `json:"trips"`
	}
	if err := json.Unmarshal(demo.PresetsIndex().Body, &idx); err != nil {
		return presetMeta{}, false
	}
	for _, t := range idx.Trips {
		if t.ID == id {
			return t, true
		}
	}
	return presetMeta{}, false
}

func clock(utc string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		return ""
	}
	return strings.ToLower(t.In(loc).Format("3:04 pm"))
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", math.Round(v)) }

func durText(sec float64) string {
	m := int(math.Round(sec / 60))
	if m < 1 {
		return "0 min"
	}
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

func money(cur string, v float64) string {
	sym := "$"
	if cur != "" && cur != "USD" {
		sym = cur + " "
	}
	return fmt.Sprintf("%s%.2f", sym, v)
}

// dayFromRoute builds one day's table from a route and its cost breakdown.
func dayFromRoute(title string, r planRoute, costs []planCost, loc *time.Location) sampleDay {
	byCharger := map[string]planBreakdown{}
	cur := ""
	if len(costs) > 0 {
		cur = costs[0].Currency
		for _, b := range costs[0].Breakdown {
			byCharger[b.ChargerName] = b
		}
	}
	d := sampleDay{Title: title}
	for _, w := range r.Waypoints {
		st := sampleStop{Name: w.Name}
		switch w.WaypointType {
		case "ORIGIN":
			st.Kind = "origin"
			st.DepartClock, st.DepartSoC = clock(w.DepartureTimeUTC, loc), pct(w.DepartureSoC)
		case "DESTINATION":
			st.Kind = "destination"
			st.ArriveClock, st.ArriveSoC = clock(w.ArrivalTimeUTC, loc), pct(w.ArrivalSoC)
		default:
			st.Kind = "charge"
			st.ArriveClock, st.ArriveSoC = clock(w.ArrivalTimeUTC, loc), pct(w.ArrivalSoC)
			st.DepartClock, st.DepartSoC = clock(w.DepartureTimeUTC, loc), pct(w.DepartureSoC)
			st.Charge = durText(w.ChargeDurationSec)
			if w.MaxPowerKW > 0 {
				st.Charge += fmt.Sprintf(" at up to %.0f kW", w.MaxPowerKW)
			}
			if b, ok := byCharger[w.Name]; ok {
				// Charger names often carry the network already ("... [Electrify America]").
				if !strings.Contains(strings.ToLower(w.Name), strings.ToLower(b.NetworkName)) {
					st.Network = b.NetworkName
				}
				st.Estimate = money(cur, b.EnergyKWh*b.UserRate) + " (estimate, " + fmt.Sprintf("%.0f kWh at %s/kWh", b.EnergyKWh, money(cur, b.UserRate)) + ")"
			}
		}
		d.Stops = append(d.Stops, st)
	}
	d.Summary = fmt.Sprintf("%.0f mi, %s driving, %s charging, arrive with %s.",
		r.TotalDriveDistanceMeters/metersPerMile, durText(r.TotalDriveDurationSec), durText(r.TotalChargingDurationSec), pct(r.ArrivalSoC))
	return d
}

func buildSamplePage(c demo.SampleTripCopy) (*samplePage, error) {
	loc, err := time.LoadLocation(sampleTripZone)
	if err != nil {
		return nil, err
	}
	blob, ok := demo.PresetPlan(c.Preset)
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", c.Preset)
	}
	var doc planDoc
	if err := json.Unmarshal(blob.Body, &doc); err != nil {
		return nil, err
	}
	meta, ok := presetMetaByID(c.Preset)
	if !ok {
		return nil, fmt.Errorf("preset %q is not in the index", c.Preset)
	}
	p := &samplePage{
		Slug: c.Slug, Path: pathSampleTrips + "/" + c.Slug,
		Title: c.Title, Heading: trimSiteSuffix(c.Title), Description: c.Description, Intro: c.Intro,
	}

	var totalSpend, homeEq float64
	cur := ""
	switch {
	case len(doc.Days) > 0:
		for i, d := range doc.Days {
			if len(d.Plan.Routes) == 0 {
				return nil, fmt.Errorf("day %d has no route", i+1)
			}
			day := dayFromRoute(fmt.Sprintf("Day %d", i+1), d.Plan.Routes[0], d.Costs, loc)
			if d.Overnight != nil {
				day.Overnite = fmt.Sprintf("Overnight at %s: %.0f hours parked on a %.0f kW Level 2 charger, adding about %.0f kWh to reach %s.",
					d.Overnight.Name, d.Overnight.ParkedHours, d.Overnight.L2KW, d.Overnight.AddedKWh, pct(d.Overnight.PostSoC))
			}
			p.Days = append(p.Days, day)
			if len(d.Costs) > 0 {
				cur = d.Costs[0].Currency
				totalSpend += d.Costs[0].DcfcSpendUserMember
				homeEq += d.Costs[0].HomeEquivalent
			}
		}
		p.Totals = fmt.Sprintf("Whole trip: %.0f mi, %s driving, %s charging on the road.",
			doc.Total.DistanceMeters/metersPerMile, durText(doc.Total.DriveDurationSec), durText(doc.Total.ChargingSec))
	case len(doc.Routes) > 0:
		p.Days = append(p.Days, dayFromRoute("The trip", doc.Routes[0], doc.Costs, loc))
		if len(doc.Costs) > 0 {
			cur = doc.Costs[0].Currency
			totalSpend, homeEq = doc.Costs[0].DcfcSpendUserMember, doc.Costs[0].HomeEquivalent
		}
		r := doc.Routes[0]
		p.Totals = fmt.Sprintf("Whole trip: %.0f mi, %s driving, %s charging on the road.",
			r.TotalDriveDistanceMeters/metersPerMile, durText(r.TotalDriveDurationSec), durText(r.TotalChargingDurationSec))
	default:
		return nil, fmt.Errorf("preset has neither routes nor days")
	}
	if totalSpend > 0 {
		p.Totals += fmt.Sprintf(" Fast charging: about %s (estimate).", money(cur, totalSpend))
		if homeEq > 0 {
			p.Totals += fmt.Sprintf(" The same energy at a home rate: about %s.", money(cur, homeEq))
		}
	}

	// Map: the polylines of the named presets (the page's own, or the legs it is made of).
	ids := c.MapPresets
	if len(ids) == 0 {
		ids = []string{c.Preset}
	}
	var lines [][][2]float64
	for _, id := range ids {
		m, ok := presetMetaByID(id)
		if !ok {
			return nil, fmt.Errorf("map preset %q is not in the index", id)
		}
		if pts := decodePolyline6(m.RoutePolyline); len(pts) > 1 {
			lines = append(lines, pts)
		}
	}
	var stops [][2]float64
	var stopKinds []string
	for _, d := range allRouteWaypoints(doc) {
		stops = append(stops, [2]float64{d.Latitude, d.Longitude})
		stopKinds = append(stopKinds, d.WaypointType)
	}
	p.MapSVG = renderRouteSVG(lines, stops, stopKinds, p.Heading)

	// Inline JSON for the SPA component.
	inline := map[string]any{
		"slug": c.Slug, "title": c.Title, "description": c.Description, "intro": c.Intro,
		"preset_id": c.Preset, "name": meta.Name, "inputs": meta.Inputs, "summary": meta.Summary,
		"route_polyline": meta.RoutePolyline, "plan": json.RawMessage(blob.Body),
		"demo_url": demoPlannerURL + c.Preset, "signup_url": signupPath,
	}
	if p.JSON, err = json.Marshal(inline); err != nil {
		return nil, err
	}
	p.Article = renderSampleArticle(p)
	p.Markdown = sampleMarkdown(p)
	p.LD = sampleLD(hostApp, p)
	return p, nil
}

func allRouteWaypoints(doc planDoc) []planWP {
	var out []planWP
	if len(doc.Days) > 0 {
		for _, d := range doc.Days {
			if len(d.Plan.Routes) > 0 {
				out = append(out, d.Plan.Routes[0].Waypoints...)
			}
		}
		return out
	}
	if len(doc.Routes) > 0 {
		return doc.Routes[0].Waypoints
	}
	return nil
}

// ---- the hub -------------------------------------------------------------

func buildSampleHub(set demo.SampleTripSet, pages []*samplePage) *samplePage {
	h := &samplePage{Slug: "", Path: pathSampleTrips, Title: set.Hub.Title, Heading: trimSiteSuffix(set.Hub.Title),
		Description: set.Hub.Description, Intro: set.Hub.Intro}
	type card struct {
		Slug        string `json:"slug"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	var cards []card
	for _, p := range pages {
		cards = append(cards, card{p.Slug, p.Heading, p.Description})
	}
	h.JSON, _ = json.Marshal(map[string]any{
		"title": set.Hub.Title, "description": set.Hub.Description, "intro": set.Hub.Intro, "trips": cards,
	})
	var b bytes.Buffer
	_ = hubTemplate.Execute(&b, map[string]any{"Heading": h.Heading, "Intro": h.Intro, "Cards": cards, "Class": articleHubDOMClass, "Note": notAffiliated, "Signup": signupPath})
	h.Article = template.HTML(b.String())
	var md strings.Builder
	md.WriteString("---\ntitle: " + fmt.Sprintf("%q", h.Title) + "\ndescription: " + fmt.Sprintf("%q", h.Description) + "\nurl: https://" + hostApp + h.Path + "\n---\n\n")
	md.WriteString("# " + h.Heading + "\n\n" + h.Intro + "\n\n")
	for _, c := range cards {
		md.WriteString("- [" + c.Title + "](https://" + hostApp + pathSampleTrips + "/" + c.Slug + "): " + c.Description + "\n")
	}
	md.WriteString("\n" + notAffiliated + "\n")
	h.Markdown = md.String()
	h.LD = sampleHubLD(hostApp, h)
	return h
}

// ---- HTML ----------------------------------------------------------------

var articleTemplate = template.Must(template.New("article").Parse(`<article class="{{.Class}}">
<nav aria-label="Breadcrumb"><a href="/">Rivolt</a> / <a href="/sample-trips">Sample trips</a></nav>
<h1>{{.Heading}}</h1>
<p>{{.Intro}}</p>
{{.Map}}
{{range .Days}}<section>
<h2>{{.Title}}</h2>
<p>{{.Summary}}</p>
<table>
<caption>{{.Title}}: stops and charge levels</caption>
<thead><tr><th scope="col">Stop</th><th scope="col">Arrive</th><th scope="col">Charge</th><th scope="col">Leave</th><th scope="col">Cost</th></tr></thead>
<tbody>
{{range .Stops}}<tr><th scope="row">{{.Name}}{{if .Network}} <small>({{.Network}})</small>{{end}}</th><td>{{if .ArriveClock}}{{.ArriveClock}}, {{.ArriveSoC}}{{end}}</td><td>{{.Charge}}</td><td>{{if .DepartClock}}{{.DepartClock}}, {{.DepartSoC}}{{end}}</td><td>{{.Estimate}}</td></tr>
{{end}}</tbody>
</table>
{{if .Overnite}}<p>{{.Overnite}}</p>{{end}}
</section>
{{end}}<p>{{.Totals}}</p>
<p><a href="{{.DemoURL}}">Open this trip in the demo planner</a> · <a href="{{.Signup}}">Plan your own trip</a></p>
<p><small>{{.Note}} Times are for a departure at 8:00 am in the sample truck's zone; costs are estimates from public charging rates.</small></p>
</article>`))

var hubTemplate = template.Must(template.New("hub").Parse(`<article class="{{.Class}}">
<nav aria-label="Breadcrumb"><a href="/">Rivolt</a></nav>
<h1>{{.Heading}}</h1>
<p>{{.Intro}}</p>
<ul>
{{range .Cards}}<li><h2><a href="/sample-trips/{{.Slug}}">{{.Title}}</a></h2><p>{{.Description}}</p></li>
{{end}}</ul>
<p><a href="{{.Signup}}">Plan your own trip</a></p>
<p><small>{{.Note}}</small></p>
</article>`))

func renderSampleArticle(p *samplePage) template.HTML {
	var b bytes.Buffer
	_ = articleTemplate.Execute(&b, map[string]any{
		"Class": articleDOMClass, "Heading": p.Heading, "Intro": p.Intro, "Map": p.MapSVG, "Days": p.Days,
		"Totals": p.Totals, "DemoURL": demoPlannerURL + presetOf(p), "Signup": signupPath, "Note": notAffiliated,
	})
	return template.HTML(b.String())
}

// presetOf is the preset id inside the inline JSON (for the demo link).
func presetOf(p *samplePage) string {
	var v struct {
		Preset string `json:"preset_id"`
	}
	_ = json.Unmarshal(p.JSON, &v)
	return v.Preset
}

func sampleMarkdown(p *samplePage) string {
	var b strings.Builder
	url := "https://" + hostApp + p.Path
	b.WriteString("---\ntitle: " + fmt.Sprintf("%q", p.Title) + "\ndescription: " + fmt.Sprintf("%q", p.Description) + "\nurl: " + url + "\n---\n\n")
	b.WriteString("# " + p.Heading + "\n\n" + p.Intro + "\n\n")
	for _, d := range p.Days {
		b.WriteString("## " + d.Title + "\n\n" + d.Summary + "\n\n")
		b.WriteString("| Stop | Arrive | Charge | Leave | Cost |\n| --- | --- | --- | --- | --- |\n")
		for _, s := range d.Stops {
			name := s.Name
			if s.Network != "" {
				name += " (" + s.Network + ")"
			}
			arr, dep := "", ""
			if s.ArriveClock != "" {
				arr = s.ArriveClock + ", " + s.ArriveSoC
			}
			if s.DepartClock != "" {
				dep = s.DepartClock + ", " + s.DepartSoC
			}
			b.WriteString("| " + mdCell(name) + " | " + arr + " | " + mdCell(s.Charge) + " | " + dep + " | " + mdCell(s.Estimate) + " |\n")
		}
		if d.Overnite != "" {
			b.WriteString("\n" + d.Overnite + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(p.Totals + "\n\n")
	b.WriteString("- [Open this trip in the demo planner](" + demoPlannerURL + presetOf(p) + ")\n- [Plan your own trip](https://" + hostApp + signupPath + ")\n\n")
	b.WriteString(notAffiliated + " Times are for a departure at 8:00 am in the sample truck's zone; costs are estimates from public charging rates.\n")
	return b.String()
}

func mdCell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "/"), "\n", " ") }

// ---- JSON-LD -------------------------------------------------------------

func ldScript(doc map[string]any) string {
	b, _ := json.Marshal(doc) // escapes <, > and &
	return `<script type="application/ld+json">` + string(b) + `</script>`
}

func sampleLD(host string, p *samplePage) string {
	url := "https://" + host + p.Path
	return ldScript(map[string]any{
		"@context": "https://schema.org",
		"@graph": []any{
			map[string]any{"@type": "Article", "headline": p.Heading, "description": p.Description, "url": url,
				"mainEntityOfPage": url, "isAccessibleForFree": true,
				"publisher": map[string]any{"@type": "Organization", "name": "Rivolt"}},
			map[string]any{"@type": "BreadcrumbList", "itemListElement": []any{
				map[string]any{"@type": "ListItem", "position": 1, "name": "Rivolt", "item": "https://" + host + "/"},
				map[string]any{"@type": "ListItem", "position": 2, "name": "Sample trips", "item": "https://" + host + pathSampleTrips},
				map[string]any{"@type": "ListItem", "position": 3, "name": p.Heading, "item": url},
			}},
		},
	})
}

func sampleHubLD(host string, p *samplePage) string {
	return ldScript(map[string]any{
		"@context": "https://schema.org",
		"@type":    "BreadcrumbList",
		"itemListElement": []any{
			map[string]any{"@type": "ListItem", "position": 1, "name": "Rivolt", "item": "https://" + host + "/"},
			map[string]any{"@type": "ListItem", "position": 2, "name": "Sample trips", "item": "https://" + host + pathSampleTrips},
		},
	})
}

// ---- the map -------------------------------------------------------------

// decodePolyline6 decodes a Google encoded polyline of precision 6 into
// (lat, lon) pairs.
func decodePolyline6(s string) [][2]float64 {
	var out [][2]float64
	var lat, lon, i int
	next := func() (int, bool) {
		var shift, result int
		for {
			if i >= len(s) {
				return 0, false
			}
			c := int(s[i]) - 63
			i++
			result |= (c & 0x1f) << shift
			shift += 5
			if c < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			return ^(result >> 1), true
		}
		return result >> 1, true
	}
	for i < len(s) {
		dlat, ok := next()
		if !ok {
			break
		}
		dlon, ok := next()
		if !ok {
			break
		}
		lat += dlat
		lon += dlon
		out = append(out, [2]float64{float64(lat) / 1e6, float64(lon) / 1e6})
	}
	return out
}

// renderRouteSVG draws the route lines and the stops as an inline SVG that needs
// no tiles, scripts or network: an equirectangular projection fitted to the box.
func renderRouteSVG(lines [][][2]float64, stops [][2]float64, kinds []string, label string) template.HTML {
	var all [][2]float64
	for _, l := range lines {
		all = append(all, l...)
	}
	all = append(all, stops...)
	if len(all) == 0 {
		return ""
	}
	minLat, maxLat, minLon, maxLon := 90.0, -90.0, 180.0, -180.0
	for _, p := range all {
		minLat, maxLat = math.Min(minLat, p[0]), math.Max(maxLat, p[0])
		minLon, maxLon = math.Min(minLon, p[1]), math.Max(maxLon, p[1])
	}
	cosLat := math.Cos((minLat + maxLat) / 2 * math.Pi / 180)
	w := (maxLon - minLon) * cosLat
	h := maxLat - minLat
	if w < 1e-6 {
		w = 1e-6
	}
	if h < 1e-6 {
		h = 1e-6
	}
	innerW, innerH := float64(sampleTripMapW-2*sampleTripMapPad), float64(sampleTripMapH-2*sampleTripMapPad)
	scale := math.Min(innerW/w, innerH/h)
	offX := sampleTripMapPad + (innerW-w*scale)/2
	offY := sampleTripMapPad + (innerH-h*scale)/2
	xy := func(p [2]float64) (float64, float64) {
		return offX + (p[1]-minLon)*cosLat*scale, offY + (maxLat-p[0])*scale
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="%s" style="width:100%%;height:auto">`,
		sampleTripMapW, sampleTripMapH, template.HTMLEscapeString("Route map: "+label))
	for _, l := range lines {
		b.WriteString(`<polyline fill="none" stroke="#1d6fb8" stroke-width="3" stroke-linejoin="round" stroke-linecap="round" points="`)
		for i, p := range l {
			x, y := xy(p)
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%.1f,%.1f", x, y)
		}
		b.WriteString(`"/>`)
	}
	for i, p := range stops {
		x, y := xy(p)
		r, fill := 5.0, "#e0821c"
		if i < len(kinds) && (kinds[i] == "ORIGIN" || kinds[i] == "DESTINATION") {
			r, fill = 7.0, "#1f2933"
		}
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.0f" fill="%s" stroke="#fff" stroke-width="2"/>`, x, y, r, fill)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// ---- serving -------------------------------------------------------------

type sampleRendered struct {
	html, md []byte
	etagHTML string
	etagMD   string
}

var (
	sampleRenderMu sync.Mutex
	sampleRender   = map[string]*sampleRendered{}
)

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// renderFor returns the final HTML and Markdown for a page on this shell. The
// shell changes with each build (bundle hashes), so the cache key includes its
// hash and a deploy gets new ETags; within a build every request is a map lookup.
func (p *samplePage) renderFor(shell []byte) *sampleRendered {
	key := p.Path + "|" + shortHash(shell)
	sampleRenderMu.Lock()
	defer sampleRenderMu.Unlock()
	if r, ok := sampleRender[key]; ok {
		return r
	}
	sp := seoPage{title: p.Title, desc: p.Description, summary: p.Intro}
	out := injectSEO(shell, hostApp, sp, p.Path)
	out = bytes.Replace(out, []byte(`<div id="root"></div>`),
		[]byte(`<div id="root">`+string(p.Article)+`</div>`+"\n    "+`<script type="application/json" id="`+inlineID(p)+`">`+string(p.JSON)+`</script>`), 1)
	out = bytes.Replace(out, []byte("</head>"), []byte("    "+p.LD+"\n  </head>"), 1)
	r := &sampleRendered{html: out, md: []byte(p.Markdown)}
	r.etagHTML, r.etagMD = shortHash(out), shortHash(r.md)
	sampleRender[key] = r
	return r
}

func inlineID(p *samplePage) string {
	if p.Slug == "" {
		return "sample-trips"
	}
	return "sample-trip"
}

// sampleTripPaths lists the page paths (hub first) for the sitemap and llms.txt.
func sampleTripPaths() []string {
	site := sampleTripSite()
	if site.hub == nil {
		return nil
	}
	out := []string{pathSampleTrips}
	for _, p := range site.pages {
		out = append(out, p.Path)
	}
	return out
}

// sampleTripInfo returns a path's title and description, for the llms.txt reading list.
func sampleTripInfo(path string) (title, desc string, ok bool) {
	site := sampleTripSite()
	if site.hub == nil {
		return "", "", false
	}
	if path == pathSampleTrips {
		return site.hub.Heading, site.hub.Description, true
	}
	if strings.HasPrefix(path, pathSampleTrips+"/") {
		if p, found := site.bySlg[strings.TrimPrefix(path, pathSampleTrips+"/")]; found {
			return p.Heading, p.Description, true
		}
	}
	return "", "", false
}

// isSampleTripPath reports whether the request path belongs to the sample-trip
// section (the hub or anything below it).
func isSampleTripPath(path string) bool {
	return path == pathSampleTrips || strings.HasPrefix(path, pathSampleTrips+"/")
}

// serveSampleTrip answers a GET or HEAD under /sample-trips on the app host. The
// shell is the app's index.html. Unknown slugs get a 404 status (no soft 404).
func serveSampleTrip(w http.ResponseWriter, r *http.Request, shell []byte) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	site := sampleTripSite()
	var page *samplePage
	switch {
	case site.hub == nil:
	case path == pathSampleTrips:
		page = site.hub
	default:
		page = site.bySlg[strings.TrimPrefix(path, pathSampleTrips+"/")]
	}
	if page == nil {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if r.Method == http.MethodGet {
			_, _ = w.Write(shell)
		}
		return
	}
	rend := page.renderFor(shell)
	md := wantsMarkdown(r.Header.Get("Accept"))
	body, etag, ctype := rend.html, rend.etagHTML, "text/html; charset=utf-8"
	if md {
		body, etag, ctype = rend.md, rend.etagMD, "text/markdown; charset=utf-8"
	}
	h := w.Header()
	h.Add("Vary", "Accept")
	h.Set("Link", linkHeader(page.Path))
	h.Set("Cache-Control", sampleTripCache)
	h.Set("ETag", etag)
	h.Set("Content-Type", ctype)
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		t := strings.TrimSpace(part)
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// sampleTripSEOPages registers the pages in seoPages (the sitemap, robots and
// llms.txt are driven from it) at start-up.
func init() {
	site := sampleTripSite()
	if site.hub == nil {
		return
	}
	pages := seoPages[hostApp]
	pages[pathSampleTrips] = seoPage{title: site.hub.Title, desc: site.hub.Description, summary: site.hub.Intro}
	for _, p := range site.pages {
		pages[p.Path] = seoPage{title: p.Title, desc: p.Description, summary: p.Intro}
	}
}
