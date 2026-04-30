package logging

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// statusRecorder wraps a ResponseWriter to capture the status code
// and bytes written for the access log line. We use chi's
// middleware.WrapResponseWriter rather than rolling our own because
// it correctly proxies http.Flusher / http.Hijacker — both used by
// the live websocket subscription endpoints.
type statusRecorder = middleware.WrapResponseWriter

// HTTPMiddleware extracts chi's RequestID into the context (so
// ContextHandler can stamp it on every log line emitted while the
// handler runs), then emits a single structured access-log entry
// when the request finishes.
//
// Place this AFTER chi's middleware.RequestID and middleware.RealIP,
// but BEFORE middleware.Recoverer so panics still get a log line.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ctx := r.Context()
		if rid := middleware.GetReqID(ctx); rid != "" {
			ctx = WithRequestID(ctx, rid)
			r = r.WithContext(ctx)
		}

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		// Use slog.Default so the ContextHandler picks up request_id
		// (and user_id once auth has run) automatically.
		AccessLog(r, ww, time.Since(start))
	})
}
