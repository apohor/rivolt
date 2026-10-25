package api

import (
	"context"

	"github.com/go-chi/chi/v5"
)

// RoutePatternFromContext is the chi route pattern of the request in ctx ("/api/vehicles/{vehicleID}/now"), for the label of a
// metric: never the path, whose ids would make the series unbounded. "none" when ctx is not a request's (a job, a background
// goroutine) and "unmatched" when no route matched (a 404). Once routing has run, the pattern is on the context object the
// handler's own context carries, so a query made from a handler sees it.
func RoutePatternFromContext(ctx context.Context) string {
	rc := chi.RouteContext(ctx)
	if rc == nil {
		return "none"
	}
	if p := rc.RoutePattern(); p != "" {
		return p
	}
	return "unmatched"
}
