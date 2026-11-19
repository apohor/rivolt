package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// demoVisitSources is the whole label set of rivolt_demo_visits_total.
// A URL parameter never becomes a label value: it is mapped onto this
// list, so the series count is fixed whatever visitors send.
var demoVisitSources = []string{"reddit", "x", "rivianforums", "youtube", "github", "other", "none"}

// DemoVisitCounter counts visits to the standalone demo by where the
// visitor came from. Nothing else is kept: no id, no IP, no referrer URL.
type DemoVisitCounter struct {
	vec *prometheus.CounterVec
}

// NewDemoVisitCounter builds the counter with every source pre-set to 0;
// register Collector with the metrics registry.
func NewDemoVisitCounter() *DemoVisitCounter {
	c := &DemoVisitCounter{vec: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rivolt_demo_visits_total",
		Help: "Visits to the standalone demo by source bucket (utm_source, else the referrer host): reddit, x, rivianforums, youtube, github, other, none.",
	}, []string{"source"})}
	for _, s := range demoVisitSources {
		c.vec.WithLabelValues(s)
	}
	return c
}

// Collector is what the metrics registry registers.
func (c *DemoVisitCounter) Collector() prometheus.Collector { return c.vec }

// demoVisitSource buckets a visit: utm_source wins, the referrer host is
// the fallback, anything unlisted is other, nothing at all is none.
func demoVisitSource(utm, referrerHost string) string {
	if utm != "" {
		return demoVisitBucket(utm, true)
	}
	if referrerHost != "" {
		return demoVisitBucket(referrerHost, false)
	}
	return "none"
}

func demoVisitBucket(v string, campaign bool) string {
	v = strings.ToLower(v)
	hostIs := func(domains ...string) bool {
		for _, d := range domains {
			if v == d || strings.HasSuffix(v, "."+d) {
				return true
			}
		}
		return false
	}
	switch {
	case campaign && (v == "reddit" || v == "x" || v == "twitter" || v == "rivianforums" || v == "youtube" || v == "github"):
		if v == "twitter" {
			return "x"
		}
		return v
	case campaign:
		return "other"
	case hostIs("reddit.com", "redd.it"):
		return "reddit"
	case hostIs("x.com", "twitter.com", "t.co"):
		return "x"
	case hostIs("rivianforums.com"):
		return "rivianforums"
	case hostIs("youtube.com", "youtu.be"):
		return "youtube"
	case hostIs("github.com"):
		return "github"
	}
	return "other"
}

// handleDemoVisits serves POST /api/events on the standalone demo. It
// reads the same batch the app sends but keeps only demo_view, as a
// count; every other event, and the visitor id, is dropped unread.
func handleDemoVisits(c *DemoVisitCounter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dntRequested(r) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req eventsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if !anonIDRe.MatchString(req.AnonID) || len(req.Events) == 0 || len(req.Events) > eventsMaxBatch {
			http.Error(w, "bad batch", http.StatusBadRequest)
			return
		}
		for _, e := range req.Events {
			if e.Name != "demo_view" {
				continue
			}
			p := sanitizeProps("demo_view", e.Props)
			utm, _ := p["utm_source"].(string)
			host, _ := p["referrer_host"].(string)
			c.vec.WithLabelValues(demoVisitSource(utm, host)).Inc()
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
