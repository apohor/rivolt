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
)

// Disabled reports whether RIVOLT_SIGNUP_DISABLED=true.
func Disabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RIVOLT_SIGNUP_DISABLED")), "true")
}

// Allow reports whether a sign-in by (username, email) may proceed
// while signups are disabled: the account already exists, or MayCreate.
func Allow(ctx context.Context, d *sql.DB, username, email string) (bool, error) {
	exists, err := db.UserExistsByUsername(ctx, d, username)
	if err != nil {
		return false, err
	}
	if exists {
		return true, nil
	}
	return MayCreate(ctx, d, email)
}

// MayCreate reports whether a sign-in by an identity with no existing
// account is still allowed to create one while signups are disabled.
// Only two cases are: the email is RIVOLT_BOOTSTRAP_ADMIN_EMAIL, or the
// users table is empty (a fresh install must be able to create its
// first admin). Both mean the first admin can never be locked out.
func MayCreate(ctx context.Context, d *sql.DB, email string) (bool, error) {
	if boot := strings.ToLower(strings.TrimSpace(os.Getenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL"))); boot != "" &&
		strings.ToLower(strings.TrimSpace(email)) == boot {
		return true, nil
	}
	n, err := db.CountUsers(ctx, d)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}
