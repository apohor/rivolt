package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

// Which iPhone builds are in use (zero-impact deploys, G5): the app sends X-Rivolt-App-Build (the build number, a plain integer) on every
// request, and each request of an app session is counted by it. A build that does not send the header is "none" (a build from before the
// header). The label is bounded: at most appBuildLabels distinct numbers per process, then "other"; anything that is not a plain integer is
// "unknown". Nothing about the user or the request is kept, only the count.

const (
	appBuildHeader = "X-Rivolt-App-Build"
	appBuildLabels = 24
)

// AppBuildCounter counts app-session requests by build.
type AppBuildCounter struct {
	vec  *prometheus.CounterVec
	mu   sync.Mutex
	seen map[string]bool
}

// NewAppBuildCounter builds the counter; register Collector with the metrics registry.
func NewAppBuildCounter() *AppBuildCounter {
	return &AppBuildCounter{
		vec: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_app_requests_by_build_total",
			Help: "Requests of iPhone app sessions by the app's build number (X-Rivolt-App-Build): none for a build that does not send it, unknown for a malformed value, other past the label bound.",
		}, []string{"build"}),
		seen: map[string]bool{},
	}
}

// Collector is what the metrics registry registers.
func (c *AppBuildCounter) Collector() prometheus.Collector { return c.vec }

// label turns a header value into a bounded label.
func (c *AppBuildCounter) label(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "none"
	}
	if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 99_999_999 || strconv.Itoa(n) != v {
		return "unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[v] {
		return v
	}
	if len(c.seen) >= appBuildLabels {
		return "other"
	}
	c.seen[v] = true
	return v
}

// appBuildMW counts the requests of app sessions. It runs after the session is known.
func appBuildMW(c *AppBuildCounter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp {
				c.vec.WithLabelValues(c.label(r.Header.Get(appBuildHeader))).Inc()
			}
			next.ServeHTTP(w, r)
		})
	}
}
