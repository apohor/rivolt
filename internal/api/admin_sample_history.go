package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
)

// SampleHistoryRunner writes the preview sample truck's older history (demo.SampleHistoryWriter).
type SampleHistoryRunner interface {
	Run(ctx context.Context, uid uuid.UUID) (demo.SampleHistoryResult, error)
}

// handleAdminSampleHistory serves POST /api/admin/sample-history {"user_id" or "username": ...}: writes more than a year of drives, charges and weather
// behind the account's recorded history. Mounted only where the sim controls are (the preview admin site); the writer checks again.
func handleAdminSampleHistory(s SampleHistoryRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UserID   string `json:"user_id"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request body"})
			return
		}
		uid, err := uuid.Parse(body.UserID)
		if name := strings.TrimSpace(body.Username); name != "" && body.UserID == "" {
			uid, err = db.UserIDFor(name), nil
		}
		if err != nil || uid == uuid.Nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "give a user_id or a username"})
			return
		}
		ctx, cancel := accountWriteContext(r)
		defer cancel()
		res, err := s.Run(ctx, uid)
		switch {
		case errors.Is(err, demo.ErrSampleHistoryRefused), errors.Is(err, demo.ErrSampleHistoryDemo):
			writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		case errors.Is(err, demo.ErrNoRecordedHistory):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not write the sample history"})
		default:
			writeJSON(w, http.StatusOK, res)
		}
	}
}
