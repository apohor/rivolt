package api

import "net/http"

// maxJSONBody is the cap applied to JSON CRUD request bodies. Every
// such payload is small (a settings blob, a push token, a trip
// request); 1 MiB is orders of magnitude above any legitimate body
// and exists only to bound a hostile or buggy unbounded decode. Bulk
// routes (import/restore) set their own larger per-handler caps.
const maxJSONBody = 1 << 20

// maxBodyBytes caps how much of a request body a handler can read. It
// wraps r.Body in http.MaxBytesReader so the first decode that exceeds
// limit fails instead of letting a hostile or buggy client stream an
// unbounded payload into memory. Mounted on the JSON CRUD groups whose
// handlers decode r.Body without their own cap; bulk routes
// (import/restore) set larger per-handler caps and must not be wrapped.
func maxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
