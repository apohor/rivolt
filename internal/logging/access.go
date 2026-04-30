package logging

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// AccessLog emits one structured slog.Info line summarising an HTTP
// request. Split out from HTTPMiddleware so tests (and future
// adapters) can call it directly. Suppresses noisy paths that would
// otherwise dominate Loki: health probes (every readiness tick) and
// the live websocket upgrade (one line per packet would be insane).
func AccessLog(r *http.Request, ww middleware.WrapResponseWriter, dur time.Duration) {
	switch r.URL.Path {
	case "/api/health":
		return
	}

	slog.LogAttrs(r.Context(), slog.LevelInfo, "http",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", ww.Status()),
		slog.Int("bytes", ww.BytesWritten()),
		slog.Duration("dur", dur),
		slog.String("remote", r.RemoteAddr),
	)
}
