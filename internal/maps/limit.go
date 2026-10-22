package maps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ceilings on one Valhalla request, so the proxy serves what the clients draw and can't be used for bulk routing.
// The web and the iPhone send at most 1500 trace points plus the two parked ends to /trace_route, and the planner's
// /route carries the origin, the destination and up to 25 charging stops.
const (
	MaxValhallaBody     = 1 << 20
	MaxTraceShapePoints = 2000
	MaxRouteLocations   = 100
)

// errTooMany marks a probe failure that is a cap (413) rather than a malformed request (400).
var errTooMany = errors.New("too many points")

// LimitValhalla refuses a body over MaxValhallaBody (413), a /trace_route with more than MaxTraceShapePoints shape
// points or a /route with more than MaxRouteLocations locations (413), and anything the counts can't see (400): a
// query string (Valhalla reads a whole request from ?json=), a body that isn't one JSON object, an encoded_polyline
// (a polyline string holds points the count can't see), and a top-level key repeated or spelled in another case
// (Go's decoder and Valhalla's may pick different copies). Our clients send none of these. What passes is handed on
// with its body intact.
func LimitValhalla(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "query parameters are not accepted", http.StatusBadRequest)
			return
		}
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
		if err := probeValhalla(r.URL.Path, body); err != nil {
			if errors.Is(err, errTooMany) {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

// probeValhalla walks the body's top-level keys: it must be exactly one JSON object, each key once, and the counted
// lists within their caps.
func probeValhalla(path string, body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("body must be a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("body must be a JSON object")
		}
		key, _ := tok.(string)
		folded := strings.ToLower(key)
		if seen[folded] {
			return fmt.Errorf("repeated key %q", key)
		}
		seen[folded] = true
		if folded == "encoded_polyline" {
			return errors.New("encoded_polyline is not accepted; send shape")
		}
		var counted string
		limit := 0
		switch {
		case path == "/trace_route" && folded == "shape":
			counted, limit = "shape", MaxTraceShapePoints
		case path == "/route" && folded == "locations":
			counted, limit = "locations", MaxRouteLocations
		}
		if counted == "" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return errors.New("body must be a JSON object")
			}
			continue
		}
		if key != counted {
			return fmt.Errorf("key %q must be spelled %q", key, counted)
		}
		var list []json.RawMessage
		if err := dec.Decode(&list); err != nil {
			return fmt.Errorf("%s must be a list", counted)
		}
		if len(list) > limit {
			return fmt.Errorf("%w: %s has %d, the limit is %d", errTooMany, counted, len(list), limit)
		}
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("body must be a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("body must be one JSON object")
	}
	return nil
}
