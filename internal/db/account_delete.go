package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// DeleteUserAccount is the account-deletion path (self-service and
// admin): DeleteUser plus the signup_requests rows filed under the
// user's email or username, matched case-insensitively, in the same
// transaction. signup_requests has no FK to users (a request exists
// before its account does), so the cascade never reached it and a
// deleted person's email, message and signup token lingered.
//
// Rollback paths (an IdP create that failed right after the rivolt row
// was written) must keep using DeleteUser: the request is still live
// there and the person retries with the same token.
func DeleteUserAccount(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	if d == nil || uid == uuid.Nil {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM signup_requests
		 WHERE lower(email) IN (
		     SELECT lower(x)
		       FROM users u, LATERAL (VALUES (u.email), (u.username)) AS v(x)
		      WHERE u.id = $1 AND x IS NOT NULL AND x <> ''
		 )`, uid); err != nil {
		return err
	}
	if err := deleteUserTx(ctx, tx, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// IdPDeleteFailure is a pending identity-provider deletion.
type IdPDeleteFailure struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	Error     string    `json:"error"`
	Attempts  int       `json:"attempts"`
	FailedAt  time.Time `json:"failed_at"`
	LastTried time.Time `json:"last_tried"`
	// AccountExists is true when a Rivolt account with this username
	// exists again (the person signed back in through the IdP identity
	// we failed to delete). Retrying would lock them out, so the retry
	// endpoint refuses it.
	AccountExists bool `json:"account_exists"`
}

// RecordIdPDeleteFailure records (or, for a username already pending,
// updates) a failed IdP deletion.
func RecordIdPDeleteFailure(ctx context.Context, d *sql.DB, username, errMsg string) error {
	if d == nil || username == "" {
		return nil
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO idp_delete_failures (username, error)
		VALUES ($1, $2)
		ON CONFLICT (lower(username)) DO UPDATE
		   SET error = EXCLUDED.error,
		       attempts = idp_delete_failures.attempts + 1,
		       last_tried = NOW()`, username, errMsg)
	return err
}

// ListIdPDeleteFailures returns pending IdP deletions, oldest first.
func ListIdPDeleteFailures(ctx context.Context, d *sql.DB) ([]IdPDeleteFailure, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.username, f.error, f.attempts, f.failed_at, f.last_tried,
		       EXISTS (SELECT 1 FROM users u WHERE lower(u.username) = lower(f.username))
		  FROM idp_delete_failures f ORDER BY f.failed_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdPDeleteFailure{}
	for rows.Next() {
		var f IdPDeleteFailure
		if err := rows.Scan(&f.ID, &f.Username, &f.Error, &f.Attempts, &f.FailedAt, &f.LastTried, &f.AccountExists); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ErrIdPFailureNotFound is returned for an unknown failure id.
var ErrIdPFailureNotFound = errors.New("idp delete failure not found")

// IdPDeleteFailureUsername returns the username of a pending failure.
func IdPDeleteFailureUsername(ctx context.Context, d *sql.DB, id uuid.UUID) (string, error) {
	var u string
	err := d.QueryRowContext(ctx, `SELECT username FROM idp_delete_failures WHERE id = $1`, id).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrIdPFailureNotFound
	}
	return u, err
}

// UsernameExists reports whether a Rivolt account with this username
// (case-insensitive) exists.
func UsernameExists(ctx context.Context, d *sql.DB, username string) (bool, error) {
	var ok bool
	err := d.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower($1))`, username).Scan(&ok)
	return ok, err
}

// ClearIdPDeleteFailure drops any pending failure for username (an IdP
// deletion for it just succeeded).
func ClearIdPDeleteFailure(ctx context.Context, d *sql.DB, username string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM idp_delete_failures WHERE lower(username) = lower($1)`, username)
	return err
}

// ResolveIdPDeleteFailure removes a failure once the IdP deletion has
// succeeded (nothing about the person is kept).
func ResolveIdPDeleteFailure(ctx context.Context, d *sql.DB, id uuid.UUID) error {
	_, err := d.ExecContext(ctx, `DELETE FROM idp_delete_failures WHERE id = $1`, id)
	return err
}
