package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// currentTermsVersion is the version of the Terms users are asked to accept:
// the date they took effect. Changing it asks everyone to accept again. The
// client gates the UI on it; no API is blocked server-side.
const currentTermsVersion = "2026-10-17"

// termsView is the "terms" object of /api/me and the accept response.
func termsView(ctx context.Context, d *sql.DB, uid uuid.UUID) map[string]any {
	t, _ := db.TermsFor(ctx, d, uid)
	out := map[string]any{"current": currentTermsVersion, "accepted": nil, "accepted_at": nil}
	if t.Version != nil {
		out["accepted"] = *t.Version
	}
	if t.At != nil {
		out["accepted_at"] = t.At.UTC().Format(time.RFC3339)
	}
	return out
}

// handleTermsAccept — POST /api/terms/accept {version}. 409 unless version
// is the current one; otherwise records it for the session user.
// Idempotent.
func handleTermsAccept(d *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if body.Version != currentTermsVersion {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "version_not_current", "current": currentTermsVersion})
			return
		}
		if d == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := db.AcceptTerms(r.Context(), d, uid, body.Version); err != nil {
			http.Error(w, "could not record acceptance", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "terms": termsView(r.Context(), d, uid)})
	}
}
