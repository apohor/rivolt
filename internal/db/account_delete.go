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
	if err := deleteUserAccountTx(ctx, tx, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteUserAccountTx is DeleteUserAccount's body inside a caller's
// transaction.
func deleteUserAccountTx(ctx context.Context, tx *sql.Tx, uid uuid.UUID) error {
	// First, before any row lock (deleteUserTx takes it again; it's reentrant).
	if err := lockArchiveUserTx(ctx, tx, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM signup_requests
		 WHERE lower(email) IN (
		     SELECT lower(x)
		       FROM users u, LATERAL (VALUES (u.email), (u.username)) AS v(x)
		      WHERE u.id = $1 AND x IS NOT NULL AND x <> ''
		 )`, uid); err != nil {
		return err
	}
	return deleteUserTx(ctx, tx, uid)
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
// updates) a failed IdP deletion and returns the row id, which is what
// logs should carry instead of the username (an email address).
func RecordIdPDeleteFailure(ctx context.Context, d *sql.DB, username, errMsg string) (uuid.UUID, error) {
	if d == nil || username == "" {
		return uuid.Nil, nil
	}
	var id uuid.UUID
	err := d.QueryRowContext(ctx, `
		INSERT INTO idp_delete_failures (username, error)
		VALUES ($1, $2)
		ON CONFLICT (lower(username)) DO UPDATE
		   SET error = EXCLUDED.error,
		       attempts = idp_delete_failures.attempts + 1,
		       last_tried = NOW()
		RETURNING id`, username, errMsg).Scan(&id)
	return id, err
}

// ClaimDueIdPDeleteFailures returns up to limit pending failures whose
// backoff has elapsed (5 min × 2^(attempts-1), capped at 24 h; the
// exponent is capped at 9 first, so a row failing daily for months can't
// overflow the interval and break the query for every row) and
// stamps them as tried now, so another replica running the same loop
// skips them (FOR UPDATE SKIP LOCKED).
func ClaimDueIdPDeleteFailures(ctx context.Context, d *sql.DB, limit int) ([]IdPDeleteFailure, error) {
	rows, err := d.QueryContext(ctx, `
		UPDATE idp_delete_failures f SET last_tried = NOW()
		 WHERE f.id IN (
		     SELECT id FROM idp_delete_failures
		      WHERE kind = 'kratos'
		        AND last_tried <= NOW() - LEAST(interval '5 minutes' * power(2, LEAST(GREATEST(attempts - 1, 0), 9)), interval '24 hours')
		      ORDER BY last_tried
		      LIMIT $1
		      FOR UPDATE SKIP LOCKED)
		 RETURNING f.id, f.username, f.error, f.attempts, f.failed_at, f.last_tried,
		           EXISTS (SELECT 1 FROM users u WHERE lower(u.username) = lower(f.username))`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdPDeleteFailure
	for rows.Next() {
		var f IdPDeleteFailure
		if err := rows.Scan(&f.ID, &f.Username, &f.Error, &f.Attempts, &f.FailedAt, &f.LastTried, &f.AccountExists); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListIdPDeleteFailures returns pending IdP deletions, oldest first.
func ListIdPDeleteFailures(ctx context.Context, d *sql.DB) ([]IdPDeleteFailure, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.username, f.error, f.attempts, f.failed_at, f.last_tried,
		       EXISTS (SELECT 1 FROM users u WHERE lower(u.username) = lower(f.username))
		  FROM idp_delete_failures f WHERE f.kind = 'kratos' ORDER BY f.failed_at`)
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
	err := d.QueryRowContext(ctx, `SELECT username FROM idp_delete_failures WHERE id = $1 AND kind = 'kratos'`, id).Scan(&u)
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
	_, err := d.ExecContext(ctx, `DELETE FROM idp_delete_failures WHERE lower(username) = lower($1) AND kind = 'kratos'`, username)
	return err
}

// ResolveIdPDeleteFailure removes a failure once the IdP deletion has
// succeeded (nothing about the person is kept).
func ResolveIdPDeleteFailure(ctx context.Context, d *sql.DB, id uuid.UUID) error {
	_, err := d.ExecContext(ctx, `DELETE FROM idp_delete_failures WHERE id = $1`, id)
	return err
}

// Apple revocation retries (0087): kind = 'apple' rows in idp_delete_failures, one per deleted account, carrying the
// account's Sign in with Apple refresh token sealed with the account id as associated data. The Kratos functions
// above only see kind = 'kratos' rows.

// AppleRevokeMaxAge and AppleRevokeMaxAttempts bound how long a queued revoke (and its sealed token) is kept.
const (
	AppleRevokeMaxAge      = 30 * 24 * time.Hour
	AppleRevokeMaxAttempts = 20
)

// AppleRevokeItem is a queued Apple revocation.
type AppleRevokeItem struct {
	ID       uuid.UUID
	UserID   uuid.UUID // the deleted account (the seal's associated data)
	Sealed   []byte
	Attempts int
}

func appleQueueKey(uid uuid.UUID) string { return "apple:" + uid.String() }

// RecordAppleRevokeFailure queues (or, for an account already queued, replaces the token of and counts another
// attempt for) an Apple revocation that failed. errMsg must not contain the token.
func RecordAppleRevokeFailure(ctx context.Context, d *sql.DB, uid uuid.UUID, sealed []byte, errMsg string) (uuid.UUID, error) {
	if d == nil || uid == uuid.Nil || len(sealed) == 0 {
		return uuid.Nil, errors.New("db: apple revoke queue needs a db, an account and a sealed token")
	}
	var id uuid.UUID
	err := d.QueryRowContext(ctx, `
		INSERT INTO idp_delete_failures (username, error, kind, sealed, seal_uid)
		VALUES ($1, $2, 'apple', $3, $4)
		ON CONFLICT (lower(username)) DO UPDATE
		   SET error = EXCLUDED.error,
		       sealed = EXCLUDED.sealed,
		       attempts = idp_delete_failures.attempts + 1,
		       last_tried = NOW()
		RETURNING id`, appleQueueKey(uid), errMsg, sealed, uid).Scan(&id)
	return id, err
}

// ClaimDueAppleRevokes returns up to limit queued Apple revocations whose backoff has elapsed (the same schedule as
// ClaimDueIdPDeleteFailures) and stamps them as tried now, so another replica skips them.
func ClaimDueAppleRevokes(ctx context.Context, d *sql.DB, limit int) ([]AppleRevokeItem, error) {
	rows, err := d.QueryContext(ctx, `
		UPDATE idp_delete_failures f SET last_tried = NOW()
		 WHERE f.id IN (
		     SELECT id FROM idp_delete_failures
		      WHERE kind = 'apple'
		        AND last_tried <= NOW() - LEAST(interval '5 minutes' * power(2, LEAST(GREATEST(attempts - 1, 0), 9)), interval '24 hours')
		      ORDER BY last_tried
		      LIMIT $1
		      FOR UPDATE SKIP LOCKED)
		 RETURNING f.id, f.seal_uid, f.sealed, f.attempts`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppleRevokeItem
	for rows.Next() {
		var it AppleRevokeItem
		if err := rows.Scan(&it.ID, &it.UserID, &it.Sealed, &it.Attempts); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AppleRevokeFailedAgain counts another failed attempt for a queued revocation.
func AppleRevokeFailedAgain(ctx context.Context, d *sql.DB, id uuid.UUID, errMsg string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE idp_delete_failures SET attempts = attempts + 1, error = $2, last_tried = NOW()
		 WHERE id = $1 AND kind = 'apple'`, id, errMsg)
	return err
}

// CancelAppleRevoke drops the queued revocation for an account that turned out not to be deleted.
func CancelAppleRevoke(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	_, err := d.ExecContext(ctx, `DELETE FROM idp_delete_failures WHERE kind = 'apple' AND lower(username) = lower($1)`, appleQueueKey(uid))
	return err
}

// ExpireAppleRevokes drops queued revocations (and their sealed tokens) older than AppleRevokeMaxAge or past
// AppleRevokeMaxAttempts, and returns how many went.
func ExpireAppleRevokes(ctx context.Context, d *sql.DB) (int64, error) {
	res, err := d.ExecContext(ctx, `
		DELETE FROM idp_delete_failures
		 WHERE kind = 'apple'
		   AND (failed_at < NOW() - make_interval(secs => $1) OR attempts >= $2)`,
		AppleRevokeMaxAge.Seconds(), AppleRevokeMaxAttempts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
