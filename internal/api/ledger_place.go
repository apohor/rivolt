package api

import (
	"context"
	"net/http"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// placeRestamper is the slice of *charges.Store the re-stamp handler
// uses, so it can be tested without Postgres.
type placeRestamper interface {
	RestampPlace(ctx context.Context, homeLat, homeLon, radiusM float64, dryRun bool) (charges.RestampResult, error)
}

// handleRestampPlace serves POST /api/charges/restamp-place[?dry_run=1]:
// after a home-location *correction* ("the old pin was wrong"),
// recompute the recorded home/away place on past charges against the
// currently saved home (scratchpad/place-restamp-contract.md). Only the
// place changes; user-priced rows and prices are untouched.
func handleRestampPlace(store placeRestamper, home func(ctx context.Context) (settings.HomeLocation, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "charges disabled"})
			return
		}
		h, err := home(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if !h.Set {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "no home location saved"})
			return
		}
		dry := r.URL.Query().Get("dry_run") == "1" || r.URL.Query().Get("dry_run") == "true"
		res, err := store.RestampPlace(r.Context(), h.Latitude, h.Longitude, rivian.HomeRadiusM, dry)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
