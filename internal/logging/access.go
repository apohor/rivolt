package logging

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// AccessLog emits one structured slog.Info line summarising an HTTP
// request. Split out from HTTPMiddleware so tests (and future
// adapters) can call it directly. Suppresses noisy paths that would
// otherwise dominate Loki: health probes (every readiness tick) and
// the live websocket upgrade (one line per packet would be insane).
//
// uid is the user_id resolved by auth middleware during the request,
// passed in explicitly because middleware-chain context updates don't
// propagate back to this outer scope. uuid.Nil = pre-auth or
// unauthenticated request; we skip the field rather than emit
// "user_id": "00000000-..." which would muddy Loki filters.
func AccessLog(r *http.Request, ww middleware.WrapResponseWriter, dur time.Duration, uid uuid.UUID) {
	switch r.URL.Path {
	case "/api/health":
		return
	}

	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", ww.Status()),
		slog.Int("bytes", ww.BytesWritten()),
		slog.Duration("dur", dur),
		slog.String("remote", r.RemoteAddr),
	}
	if uid != uuid.Nil {
		attrs = append(attrs, slog.String("user_id", uid.String()))
	}
	slog.LogAttrs(r.Context(), slog.LevelInfo, "http", attrs...)
}
