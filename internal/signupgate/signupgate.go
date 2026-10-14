// Package signupgate implements RIVOLT_SIGNUP_DISABLED: an install-wide
// switch that closes every path that can create a new account on its
// own (the /signup page and API, access requests, OIDC and Kratos
// first sign-in). Accounts that already exist, and accounts an admin
// creates, are unaffected. Default off, so hosted behaviour is
// unchanged.
package signupgate

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/oidc"
)

// Disabled reports whether RIVOLT_SIGNUP_DISABLED=true.
func Disabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RIVOLT_SIGNUP_DISABLED")), "true")
}

// firstUserLockKey serialises the "is the install empty?" check with
// the insert that ends it (an arbitrary constant; Postgres advisory
// locks are keyed by bigint).
const firstUserLockKey int64 = 0x52564c545f464952 // "RVLT_FIR"

// Allow reports whether a sign-in by (username, email) may proceed
// while signups are disabled:
//
//   - the account already exists (username is lower-cased and trimmed,
//     the same way db.UserIDFor keys users), or
//   - the email equals RIVOLT_BOOTSTRAP_ADMIN_EMAIL (compared
//     trimmed and lower-cased), or
//   - the users table is empty. That check and the insert that ends
//     it run in one transaction under an advisory lock, so two
//     concurrent first logins can't both pass. The row it inserts is
//     the same minimal row db.EnsureUserFull writes; it adds no admin
//     logic of its own (a first-ever row's role comes from the
//     migration 0015 trigger exactly as before, and the bootstrap
//     email promotion stays in EnsureUserFull).
func Allow(ctx context.Context, d *sql.DB, username, email, displayName string) (bool, error) {
	u := strings.ToLower(strings.TrimSpace(username))
	exists, err := db.UserExistsByUsername(ctx, d, u)
	if err != nil {
		return false, err
	}
	if exists {
		return true, nil
	}
	if boot := strings.ToLower(strings.TrimSpace(os.Getenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL"))); boot != "" &&
		strings.ToLower(strings.TrimSpace(email)) == boot {
		return true, nil
	}
	if d == nil || u == "" {
		return false, nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, firstUserLockKey); err != nil {
		return false, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (id, username, email, display_name)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))
		ON CONFLICT (id) DO NOTHING`,
		db.UserIDFor(u), u, strings.TrimSpace(email), strings.TrimSpace(displayName)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// NewCapGuard builds oidc.Config.CapGuard. With signups open (the
// default) it is exactly the guard the server always had: an existing
// user passes, otherwise the signup_cap flag decides (limit <= 0
// blocks, otherwise the user count must be under it). With
// RIVOLT_SIGNUP_DISABLED an unknown identity must also pass Allow.
// limit reads the current signup_cap limit.
func NewCapGuard(d *sql.DB, limit func() int) func(ctx context.Context, username, email string) error {
	return func(ctx context.Context, username, email string) error {
		exists, err := db.UserExistsByUsername(ctx, d, username)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		if Disabled() {
			ok, err := Allow(ctx, d, username, email, "")
			if err != nil {
				return err
			}
			if !ok {
				return oidc.ErrSignupDisabled
			}
			return nil
		}
		lim := limit()
		if lim <= 0 {
			return oidc.ErrCapExceeded
		}
		n, err := db.CountUsers(ctx, d)
		if err != nil {
			return err
		}
		if n >= lim {
			return oidc.ErrCapExceeded
		}
		return nil
	}
}
