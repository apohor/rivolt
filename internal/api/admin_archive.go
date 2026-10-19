package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
)

// ArchiveRestorer is archive.Restorer as the admin API uses it.
type ArchiveRestorer interface {
	Restore(ctx context.Context, uid, vid uuid.UUID, month time.Time, dryRun, wait bool) (archive.RestoreResult, error)
}

// handleAdminArchiveRestore puts one archived (user, vehicle, month) back in
// full detail: POST {user_id, vehicle_id, month: "2026-05", dry_run}. Admin
// site only, audited (user, vehicle, month, dry_run; never values). It runs
// past the request like an account deletion (accountWriteContext) and
// refuses with 409 while an archive, thinning or retention run holds the
// maintenance lock.
func handleAdminArchiveRestore(rs ArchiveRestorer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rs == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "the telemetry archive is off"})
			return
		}
		var body struct {
			UserID    string `json:"user_id"`
			VehicleID string `json:"vehicle_id"`
			Month     string `json:"month"`
			DryRun    bool   `json:"dry_run"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request body"})
			return
		}
		uid, err1 := uuid.Parse(body.UserID)
		vid, err2 := uuid.Parse(body.VehicleID)
		month, err3 := time.Parse("2006-01", body.Month)
		if err1 != nil || err2 != nil || err3 != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "user_id and vehicle_id must be ids, month YYYY-MM"})
			return
		}
		ctx, cancel := accountWriteContext(r)
		defer cancel()
		res, err := rs.Restore(ctx, uid, vid, month, body.DryRun, false)
		switch {
		case errors.Is(err, archive.ErrBusy):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		case errors.Is(err, archive.ErrNoObject):
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusOK, res)
		}
	}
}
