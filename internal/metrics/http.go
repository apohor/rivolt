package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// HTTPMiddleware records request count + latency for every handler.
// Uses chi's RoutePattern (e.g. "/api/state/{vehicleID}") rather
// than the raw URL so cardinality stays bounded as vehicles scale.
//
// Place AFTER chi route mounting (chi resolves the pattern when the
// handler runs, not before) — i.e. add it inside Router.Group / on
// individual subrouters that have routes mounted, OR rely on chi's
// behaviour of populating RouteContext as routing happens. We mount
// it at the root using a deferred capture: the recorder reads
// chi.RouteContext(r.Context()).RoutePattern() AFTER ServeHTTP, by
// which point chi has filled it in.
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			// Unmatched URLs (404s before they hit a handler) shouldn't
			// blow up cardinality with raw paths.
			route = "unmatched"
		}
		status := strconv.Itoa(ww.Status())

		m.HTTPRequestsTotal.WithLabelValues(r.Method, route, status).Inc()
		m.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
