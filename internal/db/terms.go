package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Terms is what a user accepted: Version is nil when never.
type Terms struct {
	Version *string
	At      *time.Time
}

// TermsFor returns the user's recorded acceptance (zero value when none or
// the user is unknown).
func TermsFor(ctx context.Context, d *sql.DB, uid uuid.UUID) (Terms, error) {
	var t Terms
	if d == nil || uid == uuid.Nil {
		return t, nil
	}
	var v sql.NullString
	var at sql.NullTime
	err := d.QueryRowContext(ctx,
		`SELECT terms_accepted_version, terms_accepted_at FROM users WHERE id = $1`, uid).Scan(&v, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if v.Valid {
		t.Version = &v.String
	}
	if at.Valid {
		t.At = &at.Time
	}
	return t, nil
}

// AcceptTerms records that the user accepted version. Idempotent: accepting
// the version already on file keeps the original time.
func AcceptTerms(ctx context.Context, d *sql.DB, uid uuid.UUID, version string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE users
		   SET terms_accepted_at = CASE WHEN terms_accepted_version IS NOT DISTINCT FROM $2
		                                 AND terms_accepted_at IS NOT NULL
		                                THEN terms_accepted_at ELSE NOW() END,
		       terms_accepted_version = $2
		 WHERE id = $1`, uid, version)
	return err
}
