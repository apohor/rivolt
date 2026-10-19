package api

import (
	"encoding/json"
	"errors"

	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/apohor/rivolt/internal/demo"
)

const testShell = `<!doctype html><html><head><title>Rivolt</title>
<meta name="description" content="x" />
<meta property="og:title" content="x" />
<meta property="og:description" content="x" />
<meta name="twitter:title" content="x" />
<meta name="twitter:description" content="x" />
</head><body><div id="root"></div></body></html>`

func sampleHandler() http.Handler {
	return spaHandler(fstest.MapFS{"index.html": {Data: []byte(testShell)}})
}

func getAs(h http.Handler, host, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// failingTransport fails any outbound request: serving these pages must not
// make one (no planner, router, geocoder, weather, LLM or Rivian call).
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("outbound request while serving a sample-trip page: %s", r.URL)
	return nil, errors.New("no outbound calls allowed")
}

func noOutbound(t *testing.T) {
	t.Helper()
	old, oldClient := http.DefaultTransport, http.DefaultClient.Transport
	http.DefaultTransport = failingTransport{t}
	http.DefaultClient.Transport = failingTransport{t}
	t.Cleanup(func() { http.DefaultTransport, http.DefaultClient.Transport = old, oldClient })
}

func presetStopNames(t *testing.T, preset string) []string {
	t.Helper()
	blob, ok := demo.PresetPlan(preset)
	if !ok {
		t.Fatalf("no preset %s", preset)
	}
	var doc planDoc
	if err := json.Unmarshal(blob.Body, &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range allRouteWaypoints(doc) {
		names = append(names, w.Name)
	}
	if len(names) < 2 {
		t.Fatalf("preset %s has %d waypoints", preset, len(names))
	}
	return names
}

func TestSampleTripPagesRenderEveryStop(t *testing.T) {
	noOutbound(t)
	set, err := demo.SampleTrips()
	if err != nil {
		t.Fatal(err)
	}
	h := sampleHandler()
	for _, c := range set.Trips {
		w := getAs(h, "rivolt.dev", "GET", "/sample-trips/"+c.Slug, nil)
		if w.Code != 200 {
			t.Fatalf("%s: %d", c.Slug, w.Code)
		}
		body := w.Body.String()
		for _, name := range presetStopNames(t, c.Preset) {
			if !strings.Contains(body, template_escape(name)) {
				t.Errorf("%s: stop %q missing from the rendered article", c.Slug, name)
			}
		}
		for _, want := range []string{
			`<article class="sample-trip">`, `<svg `, "<polyline", notAffiliated,
			`<link rel="canonical" href="https://rivolt.dev/sample-trips/` + c.Slug + `" />`,
			`<script type="application/ld+json">`, `"@type":"Article"`, `"@type":"BreadcrumbList"`,
			`<title>` + c.Title + `</title>`, `https://demo.rivolt.dev/trips/plan?preset=` + c.Preset, `href="/signup"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", c.Slug, want)
			}
		}
		if cc := w.Header().Get("Cache-Control"); cc != sampleTripCache {
			t.Errorf("%s: Cache-Control %q", c.Slug, cc)
		}
		if w.Header().Get("ETag") == "" || w.Header().Get("X-Robots-Tag") != "" {
			t.Errorf("%s: ETag %q, X-Robots-Tag %q", c.Slug, w.Header().Get("ETag"), w.Header().Get("X-Robots-Tag"))
		}
		// The inline JSON is what the SPA component reads.
		m := regexp.MustCompile(`<script type="application/json" id="sample-trip">(.*?)</script>`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no inline JSON", c.Slug)
		}
		var inline map[string]json.RawMessage
		if err := json.Unmarshal([]byte(m[1]), &inline); err != nil {
			t.Fatalf("%s: inline JSON: %v", c.Slug, err)
		}
		for _, k := range []string{"slug", "title", "description", "intro", "preset_id", "name", "inputs", "summary", "route_polyline", "plan", "demo_url", "signup_url"} {
			if _, ok := inline[k]; !ok {
				t.Errorf("%s: inline JSON lacks %q", c.Slug, k)
			}
		}
	}
}

// html/template escapes names (an ampersand becomes &amp;); compare the escaped form.
func template_escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "'", "&#39;", `"`, "&#34;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func TestSampleTripHubListsThePages(t *testing.T) {
	noOutbound(t)
	set, _ := demo.SampleTrips()
	w := getAs(sampleHandler(), "rivolt.dev", "GET", "/sample-trips", nil)
	if w.Code != 200 {
		t.Fatalf("hub: %d", w.Code)
	}
	body := w.Body.String()
	for _, c := range set.Trips {
		if !strings.Contains(body, `href="/sample-trips/`+c.Slug+`"`) {
			t.Errorf("hub lacks a link to %s", c.Slug)
		}
	}
	if !strings.Contains(body, `id="sample-trips"`) || !strings.Contains(body, `rel="canonical" href="https://rivolt.dev/sample-trips"`) {
		t.Error("hub lacks its inline JSON or canonical")
	}
}

func TestUnknownSampleTripIsAReal404(t *testing.T) {
	h := sampleHandler()
	for _, p := range []string{"/sample-trips/nope", "/sample-trips/la-las-vegas-two-days/extra", "/sample-trips/..%2Fprivacy", "/sample-trips/La-Mammoth-Lakes"} {
		w := getAs(h, "rivolt.dev", "GET", p, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (not the shell's 200)", p, w.Code)
		}
		if w.Header().Get("X-Robots-Tag") != "noindex" {
			t.Errorf("%s: 404 without noindex", p)
		}
	}
}

func TestSampleTripQueryIsIgnoredAndRevalidation(t *testing.T) {
	h := sampleHandler()
	a := getAs(h, "rivolt.dev", "GET", "/sample-trips/la-mammoth-lakes", nil)
	b := getAs(h, "rivolt.dev", "GET", "/sample-trips/la-mammoth-lakes?utm_source=x&preset=other", nil)
	if a.Body.String() != b.Body.String() || a.Header().Get("ETag") != b.Header().Get("ETag") {
		t.Error("a query string changed the output")
	}
	if !strings.Contains(a.Body.String(), `href="https://rivolt.dev/sample-trips/la-mammoth-lakes"`) {
		t.Error("canonical carries a query or is missing")
	}
	etag := a.Header().Get("ETag")
	if c := getAs(h, "rivolt.dev", "GET", "/sample-trips/la-mammoth-lakes", map[string]string{"If-None-Match": etag}); c.Code != http.StatusNotModified || c.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d with %d bytes, want 304 and no body", c.Code, c.Body.Len())
	}
	if hd := getAs(h, "rivolt.dev", "HEAD", "/sample-trips/la-mammoth-lakes", nil); hd.Code != 200 || hd.Body.Len() != 0 || hd.Header().Get("ETag") != etag {
		t.Errorf("HEAD: %d, %d bytes, etag %q", hd.Code, hd.Body.Len(), hd.Header().Get("ETag"))
	}
}

func TestSampleTripMarkdown(t *testing.T) {
	w := getAs(sampleHandler(), "rivolt.dev", "GET", "/sample-trips/pasadena-san-francisco", map[string]string{"Accept": "text/markdown"})
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	if !strings.Contains(body, "| Stop | Arrive | Charge | Leave | Cost |") || !strings.Contains(body, "Harris Ranch") || !strings.Contains(body, notAffiliated) {
		t.Errorf("markdown lacks the table, a stop or the not-affiliated line:\n%s", body)
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept") {
		t.Error("no Vary: Accept")
	}
	html := getAs(sampleHandler(), "rivolt.dev", "GET", "/sample-trips/pasadena-san-francisco", nil)
	if html.Header().Get("ETag") == w.Header().Get("ETag") {
		t.Error("HTML and Markdown share an ETag")
	}
}

func TestSampleTripsOnlyOnTheAppHostAndInTheDiscoveryDocs(t *testing.T) {
	h := sampleHandler()
	demoPage := getAs(h, "demo.rivolt.dev", "GET", "/sample-trips/la-mammoth-lakes", nil)
	if strings.Contains(demoPage.Body.String(), `<article class="sample-trip">`) || demoPage.Header().Get("X-Robots-Tag") != "noindex" {
		t.Error("the demo host served the article or lacked noindex")
	}
	set, _ := demo.SampleTrips()
	sm := getAs(h, "rivolt.dev", "GET", "/sitemap.xml", nil).Body.String()
	got := regexp.MustCompile(`https://rivolt\.dev/sample-trips[^<]*`).FindAllString(sm, -1)
	want := []string{"https://rivolt.dev/sample-trips"}
	for _, c := range set.Trips {
		want = append(want, "https://rivolt.dev/sample-trips/"+c.Slug)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sitemap sample-trip URLs = %v, want exactly %v", got, want)
	}
	if strings.Contains(getAs(h, "demo.rivolt.dev", "GET", "/sitemap.xml", nil).Body.String(), "sample-trips") {
		t.Error("the demo sitemap lists sample trips")
	}
	llms := getAs(h, "rivolt.dev", "GET", "/llms.txt", nil).Body.String()
	for _, u := range want {
		if !strings.Contains(llms, u) {
			t.Errorf("llms.txt lacks %s", u)
		}
	}
}

func TestDecodePolyline6RoundTrip(t *testing.T) {
	pts := [][2]float64{{34.161300, -118.167600}, {36.253866, -120.240477}, {37.795500, -122.393700}}
	got := decodePolyline6(encodePolyline6ForTest(pts))
	if len(got) != len(pts) {
		t.Fatalf("decoded %d points, want %d", len(got), len(pts))
	}
	for i := range pts {
		if math.Abs(got[i][0]-pts[i][0]) > 1e-6 || math.Abs(got[i][1]-pts[i][1]) > 1e-6 {
			t.Errorf("point %d = %v, want %v", i, got[i], pts[i])
		}
	}
	if len(decodePolyline6("")) != 0 || len(decodePolyline6("!!")) > 1 {
		t.Error("empty or garbage input did not decode safely")
	}
}

func encodePolyline6ForTest(pts [][2]float64) string {
	var b strings.Builder
	enc := func(v int) {
		v <<= 1
		if v < 0 {
			v = ^v
		}
		for v >= 0x20 {
			b.WriteByte(byte((0x20 | (v & 0x1f)) + 63))
			v >>= 5
		}
		b.WriteByte(byte(v + 63))
	}
	var pl, po int
	for _, p := range pts {
		la, lo := int(math.Round(p[0]*1e6)), int(math.Round(p[1]*1e6))
		enc(la - pl)
		enc(lo - po)
		pl, po = la, lo
	}
	return b.String()
}
