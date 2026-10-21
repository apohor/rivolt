package api

import (
	"encoding/json"
	"net/http"

	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/settings"
)

// maxOffpeakBody bounds a schedule request (8 windows are well under 2 KB).
const maxOffpeakBody = 8 << 10

// offpeakResponse is the schedule plus what the UI needs next to it: the home
// rate that prices every hour outside the windows, and the currency of all rates.
type offpeakResponse struct {
	offpeak.Schedule
	Configured bool `json:"configured"`
	// BaseRate is the flat home rate used outside the windows (0: not set).
	BaseRate float64 `json:"base_rate"`
	Currency string  `json:"currency"`
}

func offpeakView(r *http.Request, store *settings.Store) (offpeakResponse, error) {
	sch, ok, err := settings.GetOffpeak(r.Context(), store)
	if err != nil {
		return offpeakResponse{}, err
	}
	cfg, err := settings.GetChargingConfig(r.Context(), store)
	if err != nil {
		return offpeakResponse{}, err
	}
	return offpeakResponse{Schedule: sch, Configured: ok, BaseRate: cfg.HomePricePerKWh, Currency: cfg.HomeCurrency}, nil
}

// handleOffpeakGet serves GET /api/settings/charging/offpeak.
func handleOffpeakGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		out, err := offpeakView(r, store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleOffpeakPut serves PUT /api/settings/charging/offpeak: it replaces the
// schedule. A body with no windows clears it. A schedule that fails the checks is
// a 400 {"error", "field"} naming the field at fault ("windows[1].start"), and
// nothing is saved. Unknown fields are refused.
func handleOffpeakPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in offpeak.Schedule
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOffpeakBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error(), "field": ""})
			return
		}
		if dec.More() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: trailing data", "field": ""})
			return
		}
		norm, ferr := offpeak.Normalize(in)
		if ferr != nil {
			writeJSON(w, http.StatusBadRequest, ferr)
			return
		}
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := settings.SetOffpeak(r.Context(), store, norm); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := offpeakView(r, store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
