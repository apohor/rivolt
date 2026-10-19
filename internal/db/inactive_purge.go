package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Inactive-account purge (migration 0056). An account that never connected
// a vehicle is warned at day 90 and deleted at day 120 after signup. Only
// connecting Rivian (a vehicle row or a stored Rivian session) cancels it, the
// account is then out of scope; signing in does not. Admins and disabled accounts are never touched.
const (
	InactiveWarnAfter   = 90 * 24 * time.Hour
	InactiveDeleteAfter = 120 * 24 * time.Hour
	// InactiveWarnGrace is the least time between the warning and the
	// deletion: the date the email promises.
	InactiveWarnGrace = 30 * 24 * time.Hour
	// inactiveWarnLease keeps two replicas from sending the same warning.
	inactiveWarnLease = time.Hour
)

// InactiveWarning is an account due its warning email.
type InactiveWarning struct {
	UserID uuid.UUID
	Email  string
	// DeleteOn is the date the email promises: warning time plus the grace.
	DeleteOn time.Time
}

// inactiveClock is the SQL for the age of the account: time since signup.
const inactiveClock = `u.created_at`

// rivianConnected is "this user has connected Rivian": a vehicle row, or a
// stored Rivian session. The session counts on its own because a user can be
// connected without a vehicle row (a reservation holder before delivery, a
// connect whose vehicle prime failed): the warning promises deletion "unless
// you connect your Rivian", so they must never be warned or deleted.
const rivianConnected = `(EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)
	OR EXISTS (SELECT 1 FROM user_secrets s WHERE s.user_id = u.id AND s.name = 'rivian.session'))`

// inactiveScope is the common eligibility: Rivian never connected, not an
// admin, not disabled, and an email the identity provider verified. An
// unverified address is never mailed a deletion warning, and without the
// warning there is no deletion, so such an account is out of scope for the
// whole job (warn, claim, delete, counts) and for the admin list's purge_at.
const inactiveScope = `u.role <> 'admin' AND u.disabled = FALSE AND u.email_verified AND NOT ` + rivianConnected

// unwarnableScope is the accounts inactiveScope leaves out because there is no
// verified address to warn: never connected to Rivian, not an admin, not disabled,
// and either an email the identity provider never verified or no email at all.
// They hold no Rivian data (a vehicle row or a stored Rivian session would put
// them in inactiveScope's "connected" exclusion), only a sign-in identity, and are
// deleted at InactiveDeleteAfter with no warning, because a deletion warning is
// never mailed to an unverified address. Policy: privacy.md, "Retention".
const unwarnableScope = `u.role <> 'admin' AND u.disabled = FALSE
	AND (NOT u.email_verified OR COALESCE(u.email, '') = '') AND NOT ` + rivianConnected

// CancelInactiveWarnings clears the warning of accounts that connected Rivian
// (a vehicle or a stored session) since it was sent, so the record doesn't
// linger. It returns how many
// were cancelled.
func CancelInactiveWarnings(ctx context.Context, d *sql.DB) (int, error) {
	res, err := d.ExecContext(ctx, `
		UPDATE users u
		   SET inactive_warned_at = NULL, inactive_warn_claimed_at = NULL
		 WHERE u.inactive_warned_at IS NOT NULL
		   AND `+rivianConnected)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListInactiveWarnDue returns accounts whose clock is at least
// InactiveWarnAfter old, with a deliverable email and no sent warning,
// oldest first, at most limit. Candidates only: ClaimInactiveWarning each.
func ListInactiveWarnDue(ctx context.Context, d *sql.DB, limit int) ([]InactiveWarning, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT u.id, u.email FROM users u
		 WHERE `+inactiveScope+`
		   AND COALESCE(u.email, '') <> ''
		   AND u.inactive_warned_at IS NULL
		   AND `+inactiveClock+` <= now() - $1::interval
		 ORDER BY `+inactiveClock+`
		 LIMIT $2`, pgInterval(InactiveWarnAfter), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InactiveWarning
	for rows.Next() {
		var w InactiveWarning
		if err := rows.Scan(&w.UserID, &w.Email); err != nil {
			return nil, err
		}
		w.DeleteOn = time.Now().UTC().Add(InactiveWarnGrace)
		out = append(out, w)
	}
	return out, rows.Err()
}

// ClaimInactiveWarning takes the send lease for the account's warning and
// reports whether this caller won it. It re-checks eligibility, so an account
// that connected a vehicle since the list was built is not claimed.
func ClaimInactiveWarning(ctx context.Context, d *sql.DB, uid uuid.UUID) (bool, error) {
	res, err := d.ExecContext(ctx, `
		UPDATE users u SET inactive_warn_claimed_at = now()
		 WHERE u.id = $1 AND `+inactiveScope+`
		   AND COALESCE(u.email, '') <> ''
		   AND u.inactive_warned_at IS NULL
		   AND `+inactiveClock+` <= now() - $2::interval
		   AND (u.inactive_warn_claimed_at IS NULL OR u.inactive_warn_claimed_at <= now() - $3::interval)`,
		uid, pgInterval(InactiveWarnAfter), pgInterval(inactiveWarnLease))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkInactiveWarned records that the warning email was sent. Only a sent
// warning counts: a failed send leaves it NULL and the lease expires.
func MarkInactiveWarned(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	_, err := d.ExecContext(ctx, `UPDATE users SET inactive_warned_at = now() WHERE id = $1 AND inactive_warned_at IS NULL`, uid)
	return err
}

// ListInactiveDeleteDue returns the ids of warned accounts whose clock is at
// least InactiveDeleteAfter old and whose warning is at least
// InactiveWarnGrace old, oldest first, at most limit.
func ListInactiveDeleteDue(ctx context.Context, d *sql.DB, limit int) ([]uuid.UUID, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT u.id FROM users u
		 WHERE `+inactiveScope+`
		   AND u.inactive_warned_at IS NOT NULL
		   AND u.inactive_warned_at <= now() - $1::interval
		   AND `+inactiveClock+` <= now() - $2::interval
		 ORDER BY u.inactive_warned_at
		 LIMIT $3`, pgInterval(InactiveWarnGrace), pgInterval(InactiveDeleteAfter), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteInactiveAccount deletes the account through the account-delete path
// (the same cascade and signup_requests cleanup as DeleteUserAccount), but only
// if it is still eligible: the row is locked and every condition re-checked in
// the transaction, so a vehicle connected since the list was built wins. It returns the username (for the identity-provider
// removal) and whether the account was deleted.
func DeleteInactiveAccount(ctx context.Context, d *sql.DB, uid uuid.UUID) (username string, deleted bool, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// The telemetry archive lock before the row lock below, as on every
	// account-delete path: the archive job takes row locks while holding it.
	if err := lockArchiveUserTx(ctx, tx, uid); err != nil {
		return "", false, err
	}
	var name sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT u.username FROM users u
		 WHERE u.id = $1 AND `+inactiveScope+`
		   AND u.inactive_warned_at IS NOT NULL
		   AND u.inactive_warned_at <= now() - $2::interval
		   AND `+inactiveClock+` <= now() - $3::interval
		 FOR UPDATE OF u`, uid, pgInterval(InactiveWarnGrace), pgInterval(InactiveDeleteAfter)).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := deleteUserAccountTx(ctx, tx, uid); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return name.String, true, nil
}

// CountInactiveCandidates counts, for the dry run, the accounts due a warning
// and the accounts due deletion, plus warned accounts still waiting. Counts
// only: no ids, no emails.
func CountInactiveCandidates(ctx context.Context, d *sql.DB) (warnDue, deleteDue, warnedWaiting int, err error) {
	err = d.QueryRowContext(ctx, `
		SELECT
		  count(*) FILTER (WHERE COALESCE(u.email,'') <> '' AND u.inactive_warned_at IS NULL
		                     AND `+inactiveClock+` <= now() - $1::interval),
		  count(*) FILTER (WHERE u.inactive_warned_at IS NOT NULL
		                     AND u.inactive_warned_at <= now() - $2::interval
		                     AND `+inactiveClock+` <= now() - $3::interval),
		  count(*) FILTER (WHERE u.inactive_warned_at IS NOT NULL)
		  FROM users u WHERE `+inactiveScope,
		pgInterval(InactiveWarnAfter), pgInterval(InactiveWarnGrace), pgInterval(InactiveDeleteAfter)).Scan(&warnDue, &deleteDue, &warnedWaiting)
	return
}

// ListUnwarnableDeleteDue returns the ids of unwarnable accounts (see
// unwarnableScope) whose clock is at least InactiveDeleteAfter old, oldest first,
// at most limit. Candidates only: DeleteUnwarnableAccount re-checks each.
func ListUnwarnableDeleteDue(ctx context.Context, d *sql.DB, limit int) ([]uuid.UUID, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT u.id FROM users u
		 WHERE `+unwarnableScope+`
		   AND `+inactiveClock+` <= now() - $1::interval
		 ORDER BY `+inactiveClock+`
		 LIMIT $2`, pgInterval(InactiveDeleteAfter), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteUnwarnableAccount deletes an unwarnable account through the account-delete
// path, only if it still is one: the row is locked and the scope and age re-checked
// in the transaction, so a Rivian connected since the list was built, or an email
// verified since, wins. It returns the username (for the identity-provider removal)
// and whether the account was deleted.
func DeleteUnwarnableAccount(ctx context.Context, d *sql.DB, uid uuid.UUID) (username string, deleted bool, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockArchiveUserTx(ctx, tx, uid); err != nil {
		return "", false, err
	}
	var name sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT u.username FROM users u
		 WHERE u.id = $1 AND `+unwarnableScope+`
		   AND `+inactiveClock+` <= now() - $2::interval
		 FOR UPDATE OF u`, uid, pgInterval(InactiveDeleteAfter)).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := deleteUserAccountTx(ctx, tx, uid); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return name.String, true, nil
}

// CountUnwarnableDue counts, for the dry run, the unwarnable accounts due
// deletion. A count only.
func CountUnwarnableDue(ctx context.Context, d *sql.DB) (n int, err error) {
	err = d.QueryRowContext(ctx, `
		SELECT count(*) FROM users u WHERE `+unwarnableScope+` AND `+inactiveClock+` <= now() - $1::interval`,
		pgInterval(InactiveDeleteAfter)).Scan(&n)
	return
}
