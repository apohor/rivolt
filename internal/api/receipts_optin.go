package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/settings"
)

// Reading Rivian charging receipts (what Rivian billed) is the user's
// choice, off by default. The rivian_receipts flag stays the operator's kill
// switch: both must allow it. Turning it off deletes what was stored.

func handleRivianReceiptsGet(f *flags.Store, sf *settings.Factory) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{
			"opt_in":    sf.RivianReceiptsOptIn(r.Context(), uid),
			"available": f != nil && f.RivianReceipts().Enabled(uid.String()),
		})
	}
}

func handleRivianReceiptsPut(f *flags.Store, sf *settings.Factory, st *receipts.Store) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		var body struct {
			OptIn bool `json:"opt_in"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		store := sf.For(uid)
		if store == nil {
			http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := settings.SetRivianReceiptsOptIn(r.Context(), store, body.OptIn); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		if !body.OptIn && st != nil {
			if err := st.Purge(r.Context(), uid); err != nil {
				http.Error(w, "could not delete stored receipts", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{
			"opt_in":    body.OptIn,
			"available": f != nil && f.RivianReceipts().Enabled(uid.String()),
		})
	}
}
