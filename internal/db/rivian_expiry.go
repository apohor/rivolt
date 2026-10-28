package db

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

// RivianExpiryEmailsOff reports whether the user stopped the Rivian session expiry reminders.
func RivianExpiryEmailsOff(ctx context.Context, d *sql.DB, userID uuid.UUID) (bool, error) {
	var off sql.NullTime
	err := d.QueryRowContext(ctx, `SELECT rivian_expiry_emails_off_at FROM users WHERE id = $1`, userID).Scan(&off)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return off.Valid, err
}

// SetRivianExpiryEmailsOff stops or resumes the Rivian session expiry reminders. Idempotent:
// stopping again keeps the original time.
func SetRivianExpiryEmailsOff(ctx context.Context, d *sql.DB, userID uuid.UUID, off bool) error {
	q := `UPDATE users SET rivian_expiry_emails_off_at = NULL WHERE id = $1`
	if off {
		q = `UPDATE users SET rivian_expiry_emails_off_at = COALESCE(rivian_expiry_emails_off_at, now()) WHERE id = $1`
	}
	_, err := d.ExecContext(ctx, q, userID)
	return err
}
