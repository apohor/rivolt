package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/apohor/rivolt/internal/rivian"
)

// SimRunner is the preview's simulated drive and charge, as the admin API uses it (rivian.SimControl).
type SimRunner interface {
	Start(ctx context.Context, kind rivian.SimKind) (rivian.SimRun, error)
	Active(ctx context.Context) (*rivian.SimRun, error)
}

// handleAdminSimGet serves GET /api/admin/sim: the run that has not finished, or null.
func handleAdminSimGet(s SimRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run, err := s.Active(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not read the simulated run"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"active": run})
	}
}

// handleAdminSimStart serves POST /api/admin/sim/drive and /charge: books one simulated run of kind that begins in a few seconds. 409 while
// another is under way or when the sample truck cannot take one now (the reason is in the body).
func handleAdminSimStart(s SimRunner, kind rivian.SimKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run, err := s.Start(r.Context(), kind)
		var refusal *rivian.SimRefusal
		switch {
		case errors.Is(err, rivian.ErrSimBusy):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		case errors.As(err, &refusal):
			writeJSON(w, http.StatusConflict, map[string]any{"error": refusal.Reason})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not book the simulated run"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"active": run})
		}
	}
}
