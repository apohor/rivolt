package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MaxSetupNudges caps the finish-setup emails per user: one a day
// after signup, one follow-up. More than that stops being a reminder.
const MaxSetupNudges = 2

// SetupNudge is a user who never connected a Rivian account and is
// due a finish-setup email. Attempt is which email this will be
// (1 or 2) so the sender can vary the copy.
type SetupNudge struct {
	UserID  uuid.UUID
	Email   string
	Attempt int
}

// ListUsersDueForSetupNudge returns users who still have no vehicle
// (never completed a Rivian login - primeVehicles never ran, so no
// vehicles row), have a deliverable email, and are due their next
// finish-setup email:
//
//   - the first once the account is at least minAge old, and
//   - the follow-up once followUpAfter has passed since the first.
//
// Onboarding state is deliberately not a filter: many users who stall
// never finish onboarding either (they close the tab on the connect
// step), and they need the reminder most.
//
// Candidate list only; the caller must ClaimSetupNudge each one so the
// sweep is safe on every replica. Backed by users_setup_nudge_idx.
//
// minAge exists so we don't email someone who is mid-flow: created the
// account and is about to type their Rivian password. A day's grace
// catches the ones who genuinely bounced.
func ListUsersDueForSetupNudge(ctx context.Context, d *sql.DB, minAge, followUpAfter time.Duration) ([]SetupNudge, error) {
	const q = `SELECT id, email, setup_nudge_count + 1
		FROM users u
		WHERE u.setup_nudge_count < $3
		  AND u.disabled = FALSE
		  AND COALESCE(u.email, '') <> ''
		  AND u.created_at <= now() - $1::interval
		  AND (u.setup_nudged_at IS NULL OR u.setup_nudged_at <= now() - $2::interval)
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)
		ORDER BY u.created_at`
	rows, err := d.QueryContext(ctx, q, pgInterval(minAge), pgInterval(followUpAfter), MaxSetupNudges)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SetupNudge
	for rows.Next() {
		var n SetupNudge
		if err := rows.Scan(&n.UserID, &n.Email, &n.Attempt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ClaimSetupNudge atomically records that finish-setup email number
// attempt is being sent, returning true only if the claim was won. The
// WHERE re-checks eligibility and pins the count to attempt-1, so a
// connect that landed since the candidate list was built, or a peer
// replica that got there first, makes the claim a no-op. Each attempt
// can be claimed once by construction.
func ClaimSetupNudge(ctx context.Context, d *sql.DB, userID uuid.UUID, attempt int) (bool, error) {
	const q = `UPDATE users u
		SET setup_nudged_at = now(), setup_nudge_count = $2
		WHERE u.id = $1
		  AND u.setup_nudge_count = $2 - 1
		  AND u.disabled = FALSE
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)`
	res, err := d.ExecContext(ctx, q, userID, attempt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func pgInterval(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}
