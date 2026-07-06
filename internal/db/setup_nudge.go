package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SetupNudge is a user who completed onboarding but never connected a
// Rivian account, and is a candidate for the one-time finish-setup
// email.
type SetupNudge struct {
	UserID uuid.UUID
	Email  string
}

// ListUsersDueForSetupNudge returns users who finished onboarding at
// least minAge ago, still have no vehicle (never completed a Rivian
// login - primeVehicles never ran, so no vehicles row), have a
// deliverable email, and haven't been nudged yet. Candidate list only;
// the caller must ClaimSetupNudge each one so the sweep is safe on
// every replica. Backed by users_setup_nudge_idx.
//
// minAge exists so we don't email someone who is mid-flow: created the
// account, finished onboarding, and is about to type their Rivian
// password. A day's grace catches the ones who genuinely bounced.
func ListUsersDueForSetupNudge(ctx context.Context, d *sql.DB, minAge time.Duration) ([]SetupNudge, error) {
	const q = `SELECT id, email
		FROM users u
		WHERE u.onboarding_completed
		  AND u.setup_nudged_at IS NULL
		  AND u.disabled = FALSE
		  AND COALESCE(u.email, '') <> ''
		  AND u.created_at <= now() - $1::interval
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)
		ORDER BY u.created_at`
	rows, err := d.QueryContext(ctx, q, fmt.Sprintf("%d seconds", int(minAge.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SetupNudge
	for rows.Next() {
		var n SetupNudge
		if err := rows.Scan(&n.UserID, &n.Email); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ClaimSetupNudge atomically marks the one-time finish-setup email as
// sent, returning true only if the claim was won. The WHERE re-checks
// every eligibility condition so a connect that landed since the
// candidate list was built (or a peer replica that got there first)
// makes the claim a no-op. One-time by construction: setup_nudged_at
// IS NULL can only be true once.
func ClaimSetupNudge(ctx context.Context, d *sql.DB, userID uuid.UUID) (bool, error) {
	const q = `UPDATE users u
		SET setup_nudged_at = now()
		WHERE u.id = $1
		  AND u.onboarding_completed
		  AND u.setup_nudged_at IS NULL
		  AND u.disabled = FALSE
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)`
	res, err := d.ExecContext(ctx, q, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
