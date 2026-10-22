package maps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Ceilings on one Valhalla request, so the proxy serves what the clients draw and can't be used for bulk routing.
// The web and the iPhone send at most 1500 trace points plus the two parked ends to /trace_route, and the planner's
// /route carries the origin, the destination and up to 25 charging stops.
const (
	MaxValhallaBody     = 1 << 20
	MaxTraceShapePoints = 2000
	MaxRouteLocations   = 100
)

// LimitValhalla refuses a body over MaxValhallaBody (413) and a /trace_route with more than MaxTraceShapePoints
// shape points or a /route with more than MaxRouteLocations locations (413), then hands the request on with its body
// intact. A body that isn't JSON is passed on unchanged for Valhalla to reject as it always has.
func LimitValhalla(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxValhallaBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var probe struct {
			Shape     []json.RawMessage `json:"shape"`
			Locations []json.RawMessage `json:"locations"`
		}
		if json.Unmarshal(body, &probe) == nil {
			switch {
			case r.URL.Path == "/trace_route" && len(probe.Shape) > MaxTraceShapePoints:
				http.Error(w, "too many shape points", http.StatusRequestEntityTooLarge)
				return
			case r.URL.Path == "/route" && len(probe.Locations) > MaxRouteLocations:
				http.Error(w, "too many locations", http.StatusRequestEntityTooLarge)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}
