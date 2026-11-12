package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/firstscreen"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/settings"
)

// FirstScreenSource is what GET /api/first-screen reads. It is Postgres only; this handler has no Rivian client, so it cannot call Rivian.
type FirstScreenSource interface {
	FirstScreen(ctx context.Context, uid uuid.UUID, receipts bool) (firstscreen.Data, error)
}

// firstScreenPerMin is the per-user limit (shared across pods through the limiter set): the client polls every 10 s, 6 a minute, plus
// reloads. Beyond it the answer is 429.
const firstScreenPerMin = 12

// handleFirstScreen answers with the first-screen data. The receipts block is read only when the user opted in and the operator allows
// it, the same two switches as the receipts sync itself.
func handleFirstScreen(src FirstScreenSource, f *flags.Store, sf *settings.Factory) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		receipts := sf.RivianReceiptsOptIn(r.Context(), uid) && f != nil && f.RivianReceipts().Enabled(uid.String())
		d, err := src.FirstScreen(r.Context(), uid, receipts)
		if err != nil {
			http.Error(w, "first screen unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, d)
	}
}
