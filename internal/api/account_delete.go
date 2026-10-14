package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"

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
				log.Warn("clear idp delete failure", "err", cerr.Error())
			}
		}
		return idpDeleted, ""
	}
	// Logs carry the failure row's id, never the username (an email
	// address that outlives the account only while the IdP still has it).
	if d == nil {
		if log != nil {
			log.Error("idp delete failed (rivolt account already removed); no db to record it",
				"err", redactIdentifier(err.Error(), username))
		}
		return idpFailed, err.Error()
	}
	id, rerr := db.RecordIdPDeleteFailure(context.WithoutCancel(ctx), d, username, err.Error())
	if log != nil {
		safe := redactIdentifier(err.Error(), username)
		if rerr != nil {
			log.Error("idp delete failed and could not be recorded", "err", safe,
				"record_err", redactIdentifier(rerr.Error(), username))
		} else {
			log.Error("idp delete failed (rivolt account already removed); queued for retry",
				"failure_id", id.String(), "err", safe)
		}
	}
	return idpFailed, err.Error()
}

// maxLoggedErr bounds third-party error text in logs.
const maxLoggedErr = 300

// redactIdentifier removes the identifier (an email address) from error
// text before it's logged. The Kratos client's errors can echo it: the
// identity lookup puts it in the query string, and a transport error
// (*url.Error) prints the full URL; an API error includes up to 512
// bytes of response body. Both the plain and URL-encoded forms are
// removed, and the text is truncated.
func redactIdentifier(msg, identifier string) string {
	if identifier != "" {
		for _, form := range []string{identifier, url.QueryEscape(identifier), url.PathEscape(identifier)} {
			msg = replaceFold(msg, form, "<redacted>")
		}
	}
	if len(msg) > maxLoggedErr {
		cut := maxLoggedErr
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut-- // don't split a multi-byte character
		}
		msg = msg[:cut] + "…"
	}
	return msg
}

// replaceFold replaces every case-insensitive occurrence of old in s.
func replaceFold(s, old, repl string) string {
	if old == "" {
		return s
	}
	return regexp.MustCompile("(?i)"+regexp.QuoteMeta(old)).ReplaceAllLiteralString(s, repl)
}

// idpRetryBatch bounds one retry pass.
const idpRetryBatch = 20

// RunIdPDeleteRetrier retries recorded IdP deletion failures every
// interval until ctx ends, with per-row exponential backoff (see
// db.ClaimDueIdPDeleteFailures). Safe to run on every replica: rows are
// claimed with SKIP LOCKED. No-op without a database or an enabled IdP.
func RunIdPDeleteRetrier(ctx context.Context, d *sql.DB, ac idp.UserProvider, log *slog.Logger, interval time.Duration) {
	if d == nil || ac == nil || !ac.Enabled() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			retryIdPDeleteFailures(ctx, d, ac, log)
		}
	}
}

// retryIdPDeleteFailures runs one retry pass. A row whose person has a
// Rivolt account again (signed back in through the surviving identity)
// is dropped: the identity is in use, and deleting it would lock them
// out.
func retryIdPDeleteFailures(ctx context.Context, d *sql.DB, ac idp.UserProvider, log *slog.Logger) {
	due, err := db.ClaimDueIdPDeleteFailures(ctx, d, idpRetryBatch)
	if err != nil {
		if log != nil {
			log.Warn("idp delete retry: claim failed", "err", err.Error())
		}
		return
	}
	for _, f := range due {
		if f.AccountExists {
			if err := db.ResolveIdPDeleteFailure(ctx, d, f.ID); err == nil && log != nil {
				log.Info("idp delete retry: account exists again; dropped the failure", "failure_id", f.ID.String())
			}
			continue
		}
		if status, _ := deleteFromIdP(ctx, d, ac, f.Username, log); status == idpDeleted {
			if err := db.ResolveIdPDeleteFailure(ctx, d, f.ID); err == nil && log != nil {
				log.Info("idp delete retry: resolved", "failure_id", f.ID.String(), "attempts", f.Attempts)
			}
		}
	}
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
			// The identity is in use again: nothing left to clean up.
			if err := db.ResolveIdPDeleteFailure(r.Context(), d, id); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "a Rivolt account with this username exists again; the failure was dropped. Delete that account to remove the identity.",
				"dropped": true,
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
