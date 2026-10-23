package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/auth"
)

// trailerOverrider is the slice of *drives.Store the handler uses, so it can be tested without Postgres.
type trailerOverrider interface {
	SetTrailerOverride(ctx context.Context, externalID string, v *bool) (bool, error)
}

// handleDriveTrailerPatch serves PATCH /api/drives/{id}/trailer {"trailer": true|false|null}: the user's own
// correction of whether a trailer was connected on one of their drives (Sprint 8 item 7). The recorder's reading is
// the trailer connector, not the load: a bike rack with lights plugged in can read "present", so the user can say
// otherwise. true or false is "set by you" (a later re-recording or repair never changes it); null hands the drive
// back to what was recorded. Web only: not on the app write allowlist. View-as can't write. The audit line names
// the field and the action, never a place or a position.
func handleDriveTrailerPatch(store trailerOverrider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "drives disabled"})
			return
		}
		if auth.IsImpersonating(r.Context()) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "not available while viewing as another user"})
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id"})
			return
		}
		// The key must be present: {} is a mistake, not a request to clear the correction.
		var raw map[string]json.RawMessage
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&raw); err != nil || len(raw) != 1 || trailingJSON(dec) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body must be {\"trailer\": true|false|null}"})
			return
		}
		val, ok := raw["trailer"]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body must be {\"trailer\": true|false|null}"})
			return
		}
		var v *bool
		switch s := string(bytes.TrimSpace(val)); s {
		case "true":
			t := true
			v = &t
		case "false":
			f := false
			v = &f
		case "null":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "trailer must be true, false or null"})
			return
		}
		found, err := store.SetTrailerOverride(r.Context(), id, v)
		if err != nil {
			slog.ErrorContext(r.Context(), "drive trailer override", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		action := "set"
		if v == nil {
			action = "cleared"
		}
		slog.InfoContext(r.Context(), "drive override saved", "drive", id, "fields", []string{"trailer_override"}, "action", action)
		writeJSON(w, http.StatusOK, map[string]any{"trailer_override": v})
	}
}

// trailingJSON reports whether anything but whitespace follows the value just decoded.
func trailingJSON(dec *json.Decoder) bool {
	_, err := dec.Token()
	return err != io.EOF
}
