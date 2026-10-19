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
