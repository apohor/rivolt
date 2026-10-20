package api

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/apohor/rivolt/internal/demo"
)

type failTransport struct{}

func (failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("outbound call to " + r.URL.String())
}

var reTripJSON = regexp.MustCompile(`(?s)<script type="application/json" id="sample-trip">(.*?)</script>`)

// The sample-trip pages: rendered from the presets, cached, a real 404 for
// an unknown slug, Markdown for LLM crawlers, in the sitemap and llms.txt,
// and no outbound call on any request.
func TestSampleTripPages(t *testing.T) {
	shell, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := spaHandler(fstest.MapFS{"index.html": {Data: shell}})
	saved := http.DefaultTransport
	http.DefaultTransport = failTransport{}
	t.Cleanup(func() { http.DefaultTransport = saved })

	for _, p := range demo.SampleTripPages {
		path := "/sample-trips/" + p.Slug
		trip, ok := demo.SampleTripBySlug(p.Slug)
		if !ok {
			t.Fatalf("%s: no model", p.Slug)
		}
		t.Run(p.Slug, func(t *testing.T) {
			rec := seoGet(t, h, "rivolt.dev", path)
			b := rec.Body.String()
			if rec.Code != http.StatusOK || rec.Header().Get("X-Robots-Tag") != "" {
				t.Fatalf("status %d, robots %q", rec.Code, rec.Header().Get("X-Robots-Tag"))
			}
			for _, want := range []string{
				"<title>" + html.EscapeString(p.Title) + "</title>",
				`<link rel="canonical" href="https://rivolt.dev` + path + `" />`,
				`<article id="sample-trip-article">`,
				`<svg role="img"`,
				`"@type":"BreadcrumbList"`,
				"https://demo.rivolt.dev/trips/plan?preset=" + p.PresetID,
				`href="/signup"`,
				"not affiliated with Rivian",
			} {
				if !strings.Contains(b, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, d := range trip.Days {
				for _, s := range d.Stops {
					if !strings.Contains(b, html.EscapeString(s.Name)) {
						t.Errorf("stop %q not in the article", s.Name)
					}
				}
			}
			m := reTripJSON.FindStringSubmatch(b)
			if m == nil {
				t.Fatal("no inline page model")
			}
			var got demo.SampleTrip
			if err := json.Unmarshal([]byte(m[1]), &got); err != nil {
				t.Fatal(err)
			}
			if got.Slug != p.Slug || len(got.Days) != len(trip.Days) || len(got.Route) != len(trip.Route) {
				t.Errorf("inline model %s: %d days, %d points", got.Slug, len(got.Days), len(got.Route))
			}
			if strings.Count(b, "<title>") != 1 || strings.Count(b, `rel="canonical"`) != 1 {
				t.Error("title or canonical duplicated")
			}
			etag := rec.Header().Get("ETag")
			if etag == "" || rec.Header().Get("Cache-Control") != "no-cache" {
				t.Errorf("etag %q cache %q", etag, rec.Header().Get("Cache-Control"))
			}
			req := httptest.NewRequest(http.MethodGet, "http://rivolt.dev"+path, nil)
			req.Header.Set("If-None-Match", etag)
			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, req)
			if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
				t.Errorf("revalidation: %d, %d bytes", rec2.Code, rec2.Body.Len())
			}
			head := httptest.NewRecorder()
			h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "http://rivolt.dev"+path, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 {
				t.Errorf("HEAD: %d, %d bytes", head.Code, head.Body.Len())
			}
			mdReq := httptest.NewRequest(http.MethodGet, "http://rivolt.dev"+path, nil)
			mdReq.Header.Set("Accept", "text/markdown")
			md := httptest.NewRecorder()
			h.ServeHTTP(md, mdReq)
			if !strings.HasPrefix(md.Header().Get("Content-Type"), "text/markdown") || !strings.Contains(md.Body.String(), "| Stop | Network |") {
				t.Errorf("markdown: %q", md.Body.String())
			}
		})
	}

	t.Run("hub", func(t *testing.T) {
		rec := seoGet(t, h, "rivolt.dev", "/sample-trips/")
		b := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(b, `<article id="sample-trips-article">`) || !strings.Contains(b, `id="sample-trips"`) {
			t.Fatalf("hub: %d", rec.Code)
		}
		for _, p := range demo.SampleTripPages {
			if !strings.Contains(b, `href="/sample-trips/`+p.Slug+`"`) {
				t.Errorf("hub doesn't link %s", p.Slug)
			}
		}
	})
	t.Run("unknown slug is a 404", func(t *testing.T) {
		rec := seoGet(t, h, "rivolt.dev", "/sample-trips/nowhere")
		if rec.Code != http.StatusNotFound || rec.Header().Get("X-Robots-Tag") != "noindex" {
			t.Errorf("status %d robots %q", rec.Code, rec.Header().Get("X-Robots-Tag"))
		}
	})
	t.Run("not on the demo host", func(t *testing.T) {
		b := seoGet(t, h, "demo.rivolt.dev", "/sample-trips/"+demo.SampleTripPages[0].Slug).Body.String()
		if strings.Contains(b, "sample-trip-article") {
			t.Error("the demo host serves the page")
		}
	})
	t.Run("sitemap and llms.txt", func(t *testing.T) {
		sm := seoGet(t, h, "rivolt.dev", "/sitemap.xml").Body.String()
		llms := seoGet(t, h, "rivolt.dev", "/llms.txt").Body.String()
		for _, p := range sampleTripPaths() {
			if !strings.Contains(sm, "<loc>https://rivolt.dev"+p+"</loc>") {
				t.Errorf("sitemap lacks %s", p)
			}
			if !strings.Contains(llms, "https://rivolt.dev"+p+")") {
				t.Errorf("llms.txt lacks %s", p)
			}
		}
		if strings.Contains(seoGet(t, h, "demo.rivolt.dev", "/sitemap.xml").Body.String(), "sample-trips") {
			t.Error("the demo sitemap lists the pages")
		}
	})
}

// A trip without a route shape renders no map rather than panicking (DA).
func TestRouteSVGEmptyRoute(t *testing.T) {
	for _, r := range [][][2]float64{nil, {{34, -118}}} {
		if got := routeSVG(demo.SampleTrip{Title: "x", Route: r}); got != "" {
			t.Errorf("route of %d points: %q", len(r), got)
		}
	}
}

// The estimate always names the month the third-party price was seen (DA): on the
// page, the markdown and the JSON totals alike.
func TestTripCostLineNamesTheObservationMonth(t *testing.T) {
	partial := demo.SampleTripTotal{ChargeStops: 3, PricedStops: 2, UnpricedStops: 1, DCFCCostLow: 40, DCFCCostHigh: 52.5, PriceMonth: "2026-10"}
	got := tripCostLine(partial)
	for _, want := range []string{"$40.00 to $52.50", "2 of 3", "typical price seen by DCFC Tracker, Oct 2026", "shown at the charger"} {
		if !strings.Contains(got, want) {
			t.Errorf("partial line %q lacks %q", got, want)
		}
	}
	all := demo.SampleTripTotal{ChargeStops: 2, PricedStops: 2, DCFCCostLow: 30, DCFCCostHigh: 30, PriceMonth: "2026-09"}
	if got := tripCostLine(all); !strings.Contains(got, "About $30.00 fast charging") || !strings.Contains(got, "DCFC Tracker, Sep 2026") {
		t.Errorf("complete line = %q", got)
	}
	if got := tripCostLine(demo.SampleTripTotal{ChargeStops: 2, UnpricedStops: 2}); strings.Contains(got, "$") || !strings.Contains(got, "shown at each charger") {
		t.Errorf("no-price line = %q, want no dollar figure", got)
	}
	for _, slug := range []string{"la-las-vegas", "la-las-vegas-two-days"} {
		tr, ok := demo.SampleTripBySlug(slug)
		if !ok {
			continue
		}
		if tr.Totals.PricedStops > 0 && tr.Totals.PriceMonth == "" {
			t.Errorf("%s: priced stops but no price month in the totals", slug)
		}
		if md := tripMarkdown(tr, "/x"); tr.Totals.PricedStops > 0 && !strings.Contains(md, "DCFC Tracker, ") {
			t.Errorf("%s: markdown does not name the month", slug)
		}
	}
}
