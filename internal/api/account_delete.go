package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/idp"
)

// IdP deletion outcomes reported by the account-delete endpoints.
const (
	idpDeleted = "deleted"
	idpFailed  = "failed"
	idpSkipped = "skipped" // no IdP configured, or no username to delete
)

// deleteFromIdP removes username from the identity provider after the
// rivolt rows are gone. A failure is logged and recorded in
// idp_delete_failures so an admin can see and retry it; it never fails
// the request, because the account is already deleted on our side.
func deleteFromIdP(ctx context.Context, d *sql.DB, ac idp.UserProvider, username string, log *slog.Logger) (status, errMsg string) {
	if ac == nil || !ac.Enabled() || username == "" {
		return idpSkipped, ""
	}
	err := ac.DeleteUser(ctx, username)
	if err == nil {
		if d != nil {
			if cerr := db.ClearIdPDeleteFailure(context.WithoutCancel(ctx), d, username); cerr != nil && log != nil {
				log.Warn("clear idp delete failure", "username", username, "err", cerr.Error())
			}
		}
		return idpDeleted, ""
	}
	if log != nil {
		log.Warn("idp delete failed (rivolt account already removed); recorded for admin retry",
			"username", username, "err", err.Error())
	}
	if d != nil {
		if rerr := db.RecordIdPDeleteFailure(context.WithoutCancel(ctx), d, username, err.Error()); rerr != nil && log != nil {
			log.Error("record idp delete failure", "username", username, "err", rerr.Error())
		}
	}
	return idpFailed, err.Error()
}

// handleAdminIdPFailures — GET /api/admin/idp-delete-failures: pending
// identity-provider deletions, oldest first.
func handleAdminIdPFailures(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		out, err := db.ListIdPDeleteFailures(r.Context(), d)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleAdminIdPFailureRetry — POST /api/admin/idp-delete-failures/{id}/retry.
// 200 {"resolved": true} and the row is removed when the IdP deletion
// succeeds (a missing identity counts as success); 502 with the error
// (and attempts bumped) when it fails again; 409 when a Rivolt account
// with that username exists again.
func handleAdminIdPFailureRetry(d *sql.DB, ac idp.UserProvider, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		if ac == nil || !ac.Enabled() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "identity provider not configured"})
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid id"})
			return
		}
		username, err := db.IdPDeleteFailureUsername(r.Context(), d, id)
		if errors.Is(err, db.ErrIdPFailureNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// The person may have signed back in through the identity we
		// failed to delete, re-creating a Rivolt account. Deleting the
		// identity now would lock them out of it.
		exists, err := db.UsernameExists(r.Context(), d, username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if exists {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "a Rivolt account with this username exists again; delete that account instead",
			})
			return
		}
		if status, msg := deleteFromIdP(r.Context(), d, ac, username, log); status != idpDeleted {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": msg})
			return
		}
		if err := db.ResolveIdPDeleteFailure(r.Context(), d, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"resolved": true})
	}
}
