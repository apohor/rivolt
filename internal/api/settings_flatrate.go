package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/apohor/rivolt/internal/settings"
)

// flatRateBody is both the request and the response of /api/settings/charging/flat-rate.
type flatRateBody struct {
	FlatRate bool `json:"flat_rate"`
}

// handleFlatRateGet serves GET /api/settings/charging/flat-rate.
func handleFlatRateGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		on, err := settings.GetFlatRate(r.Context(), store)
		if err != nil {
			http.Error(w, "could not read", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, flatRateBody{FlatRate: on})
	}
}

// handleFlatRatePut serves PUT /api/settings/charging/flat-rate {"flat_rate": bool}.
// The body is strict: one object, the one field (present, a bool), nothing else. The iPhone app
// may make this write (app_write_allowlist.go; DA ruling Oct 26: a bool that only changes the
// savings and off-peak estimates, and a phone-only owner could not switch back otherwise), but
// only through this route: the app's PATCH /api/settings/charging still refuses the field by
// name. 30 a minute per user; one audit line per change (logSettingsWrite): who, from where,
// which field, no value.
func handleFlatRatePut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FlatRate *bool `json:"flat_rate"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid body: one JSON object only", http.StatusBadRequest)
			return
		}
		if in.FlatRate == nil {
			http.Error(w, "flat_rate is required: true or false", http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := settings.SetFlatRate(r.Context(), store, *in.FlatRate); err != nil {
			slog.ErrorContext(r.Context(), "settings write failed", "group", "charging.flat_rate", "err", err.Error())
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		logSettingsWrite(r, "charging.flat_rate", "flat_rate")
		writeJSON(w, http.StatusOK, flatRateBody{FlatRate: *in.FlatRate})
	}
}
