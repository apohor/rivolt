package db

import (
	"context"
	"database/sql"
	"fmt"
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
			    needs_reauth_at = NULL,
			    needs_reauth_notified_at = NULL
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

// RaiseNeedsReauth sets the gate and reports whether THIS call flipped
// it from false to true (the rising edge). The UPDATE is guarded on
// `needs_reauth = FALSE`, so when two replicas classify the same user
// concurrently only the one that wins the transition gets transitioned
// = true. Callers use that to fire a one-time side effect (the re-auth
// email) without double-sending across pods. The reason/at columns are
// only written on the winning transition; a user already flagged keeps
// their original reason, which is fine — the flag, not the text, is
// load-bearing.
func RaiseNeedsReauth(ctx context.Context, d *sql.DB, userID uuid.UUID, reason string) (transitioned bool, err error) {
	//
	// needs_reauth_notified_at is stamped on the same rising edge so a
	// fresh transition reads as "just notified": the sink fires the
	// one-shot email, and the periodic re-nudge sweep leaves the user
	// alone until a full cooldown passes. Users flagged before this
	// column existed have it NULL, which the sweep treats as "never
	// notified" and picks up immediately — the intended backfill-free
	// recovery.
	const q = `UPDATE users
		SET needs_reauth = TRUE,
		    needs_reauth_reason = $2,
		    needs_reauth_at = $3,
		    needs_reauth_notified_at = $3
		WHERE id = $1 AND needs_reauth = FALSE`
	res, err := d.ExecContext(ctx, q, userID, reason, time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReauthNudge is a user still stuck needing Rivian re-auth who is a
// candidate for a (re-)notification email.
type ReauthNudge struct {
	UserID uuid.UUID
	Reason string
}

// ListUsersDueForReauthNudge returns users still flagged needs_reauth
// whose last nudge is either absent (NULL — includes everyone stranded
// before the email feature shipped) or older than cooldown. This is the
// candidate list only; the caller must ClaimReauthNudge each one before
// sending, which is what makes the sweep safe to run on every replica.
// The partial index users_needs_reauth_idx keeps this scan proportional
// to the flagged set, not the whole users table.
func ListUsersDueForReauthNudge(ctx context.Context, d *sql.DB, cooldown time.Duration) ([]ReauthNudge, error) {
	const q = `SELECT id, COALESCE(needs_reauth_reason, '')
		FROM users
		WHERE needs_reauth
		  AND (needs_reauth_notified_at IS NULL
		       OR needs_reauth_notified_at <= now() - $1::interval)
		ORDER BY needs_reauth_at NULLS FIRST`
	rows, err := d.QueryContext(ctx, q, fmt.Sprintf("%d seconds", int(cooldown.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReauthNudge
	for rows.Next() {
		var n ReauthNudge
		if err := rows.Scan(&n.UserID, &n.Reason); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ClaimReauthNudge atomically records that this pod is about to send a
// stuck-session nudge, returning true only if the claim was won. The
// UPDATE's WHERE re-checks the due predicate, so when two replicas race
// on the same user Postgres' row lock lets exactly one flip
// needs_reauth_notified_at and send; the loser sees 0 rows and skips.
// Same rising-edge dedup shape as RaiseNeedsReauth, applied to the
// sweep instead of the classifier.
func ClaimReauthNudge(ctx context.Context, d *sql.DB, userID uuid.UUID, cooldown time.Duration) (bool, error) {
	const q = `UPDATE users
		SET needs_reauth_notified_at = now()
		WHERE id = $1
		  AND needs_reauth
		  AND (needs_reauth_notified_at IS NULL
		       OR needs_reauth_notified_at <= now() - $2::interval)`
	res, err := d.ExecContext(ctx, q, userID, fmt.Sprintf("%d seconds", int(cooldown.Seconds())))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
