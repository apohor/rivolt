package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// SetNeedsReauth persists the per-user re-auth gate to Postgres.
//
// reason is stored verbatim in users.needs_reauth_reason for the
// UI to render; pass an empty string to clear the flag (the row
// is updated in one statement so the UI never sees a row with
// needs_reauth = true and an empty reason, or vice versa).
//
// Best-effort by design: the caller is a LiveClient sink running
// on the hot path, and a Postgres blip shouldn't mask the
// original upstream error. Returned errors are for logging only.
func SetNeedsReauth(ctx context.Context, d *sql.DB, userID uuid.UUID, reason string) error {
	if reason == "" {
		const clear = `UPDATE users
			SET needs_reauth = FALSE,
			    needs_reauth_reason = NULL,
			    needs_reauth_at = NULL
			WHERE id = $1`
		_, err := d.ExecContext(ctx, clear, userID)
		return err
	}
	const raise = `UPDATE users
		SET needs_reauth = TRUE,
		    needs_reauth_reason = $2,
		    needs_reauth_at = $3
		WHERE id = $1`
	_, err := d.ExecContext(ctx, raise, userID, reason, time.Now().UTC())
	return err
}

// GetNeedsReauth reads the current needs_reauth state for a user.
// Used by main.go at startup to prime the LiveClient's in-memory
// mirror so a restart doesn't silently let a locked-out user's
// background jobs hammer Rivian again.
//
// A missing user row is treated as "not locked" rather than an
// error — the upstream call will fail on its own if the user
// really doesn't exist.
func GetNeedsReauth(ctx context.Context, d *sql.DB, userID uuid.UUID) (bool, string, error) {
	const q = `SELECT needs_reauth, COALESCE(needs_reauth_reason, '')
		FROM users WHERE id = $1`
	var needs bool
	var reason string
	err := d.QueryRowContext(ctx, q, userID).Scan(&needs, &reason)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	return needs, reason, err
}
