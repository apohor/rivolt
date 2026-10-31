package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// R2ArrivalDue is a user who asked for the R2 arrival-month email and may be due it.
type R2ArrivalDue struct {
	UserID uuid.UUID
	Email  string
	// Month is the chosen month, the first of it, in UTC.
	Month time.Time
}

// r2ArrivalEligible is who may get the email: a deliverable VERIFIED address that has not opted out of engagement
// emails (emailable), and no vehicle connected yet.
const r2ArrivalEligible = `p.r2_arrival_month IS NOT NULL
		  AND ` + emailable + `
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)`

// ListR2ArrivalDue returns the users whose chosen month is in [from, to] (first-of-month dates) and who are eligible. Candidate
// list only: the job picks the moment in the user's time zone and Engage claims each send.
func ListR2ArrivalDue(ctx context.Context, d *sql.DB, from, to time.Time) ([]R2ArrivalDue, error) {
	rows, err := d.QueryContext(ctx, `SELECT u.id, u.email, p.r2_arrival_month
		FROM engagement_prefs p JOIN users u ON u.id = p.user_id
		WHERE `+r2ArrivalEligible+` AND p.r2_arrival_month BETWEEN $1::date AND $2::date
		ORDER BY p.r2_arrival_month, u.id
		LIMIT `+"500", from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []R2ArrivalDue
	for rows.Next() {
		var r R2ArrivalDue
		if err := rows.Scan(&r.UserID, &r.Email, &r.Month); err != nil {
			return nil, err
		}
		r.Month = time.Date(r.Month.Year(), r.Month.Month(), 1, 0, 0, 0, 0, time.UTC)
		out = append(out, r)
	}
	return out, rows.Err()
}

// R2ArrivalStillWanted is the send-time re-check: the user still has this month set, still has a verified address, has not turned
// engagement emails off and still has no vehicle. A change since the list was built wins.
func R2ArrivalStillWanted(ctx context.Context, d *sql.DB, uid uuid.UUID, month time.Time) (bool, error) {
	var one int
	err := d.QueryRowContext(ctx, `SELECT 1 FROM engagement_prefs p JOIN users u ON u.id = p.user_id
		WHERE u.id = $1 AND p.r2_arrival_month = $2::date AND `+r2ArrivalEligible, uid, month.Format("2006-01-02")).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// ClearR2ArrivalMonth removes the user's chosen month (the email's one-click link, and Settings' Delete). Idempotent.
func ClearR2ArrivalMonth(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	_, err := d.ExecContext(ctx, `UPDATE engagement_prefs SET r2_arrival_month = NULL, updated_at = NOW() WHERE user_id = $1 AND r2_arrival_month IS NOT NULL`, uid)
	return err
}
