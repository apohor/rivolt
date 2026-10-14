//go:build integration

// RIVOLT_SIGNUP_DISABLED first sign-in rule against real Postgres.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/signupgate"
)

func openGateDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return ctx, pool
}

func cleanUsers(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	if _, err := pool.ExecContext(ctx, `DELETE FROM users`); err != nil {
		t.Fatalf("clean users: %v", err)
	}
}

// With the flag off the guard is exactly the old signup_cap guard:
// the empty-install exception does not exist and the limit decides.
func TestSignupGate_FlagOffGuardIsTheOldCapGuard(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "")
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "")
	limit := 0
	guard := signupgate.NewCapGuard(pool, func() int { return limit })

	// Empty table, limit 0: blocked (no first-user exception off-flag).
	if err := guard(ctx, "off-first@example.com", "off-first@example.com", true); !errors.Is(err, oidc.ErrCapExceeded) {
		t.Fatalf("empty install, limit 0: %v, want ErrCapExceeded", err)
	}
	limit = 2
	if err := guard(ctx, "off-first@example.com", "off-first@example.com", true); err != nil {
		t.Fatalf("under the cap: %v, want nil", err)
	}
	if _, err := db.EnsureUser(ctx, pool, "off-a@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureUser(ctx, pool, "off-b@example.com"); err != nil {
		t.Fatal(err)
	}
	// Two users, limit 2: at the cap, new identity blocked, existing passes.
	if err := guard(ctx, "off-new@example.com", "off-new@example.com", true); !errors.Is(err, oidc.ErrCapExceeded) {
		t.Fatalf("at the cap: %v, want ErrCapExceeded", err)
	}
	if err := guard(ctx, "OFF-A@example.com ", "off-a@example.com", true); err != nil {
		t.Fatalf("existing user (case/space): %v, want nil", err)
	}
	limit = 0
	if err := guard(ctx, "off-a@example.com", "off-a@example.com", true); err != nil {
		t.Fatalf("existing user, limit 0: %v, want nil", err)
	}
	// The flag-off guard never inserts a row.
	if n, _ := db.CountUsers(ctx, pool); n != 2 {
		t.Fatalf("users = %d, want 2 (guard must not create rows)", n)
	}
}

func TestSignupGate_FlagOnRules(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "true")
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "")
	guard := signupgate.NewCapGuard(pool, func() int { return 100 }) // the cap would allow

	// Empty install, no bootstrap email: the first sign-in is let in
	// and its row exists.
	if err := guard(ctx, "on-first@example.com", "on-first@example.com", false); err != nil {
		t.Fatalf("empty install: %v, want nil", err)
	}
	if ex, _ := db.UserExistsByUsername(ctx, pool, "on-first@example.com"); !ex {
		t.Fatal("first user row not created under the lock")
	}
	// Now strangers are refused even though the cap has room.
	if err := guard(ctx, "on-stranger@example.com", "on-stranger@example.com", true); !errors.Is(err, oidc.ErrSignupDisabled) {
		t.Fatalf("stranger: %v, want ErrSignupDisabled", err)
	}
	// Existing and admin-created users sign in.
	if _, err := db.EnsureUser(ctx, pool, "on-invited@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := guard(ctx, "ON-invited@example.com ", "on-invited@example.com", false); err != nil {
		t.Fatalf("admin-created: %v, want nil", err)
	}
	// Bootstrap email: verified + normalised matches; unverified never.
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "  Boss@Example.COM ")
	if err := guard(ctx, "boss@example.com", "BOSS@example.com ", true); err != nil {
		t.Fatalf("verified bootstrap admin: %v, want nil", err)
	}
	if err := guard(ctx, "boss2@example.com", "boss@example.com", false); !errors.Is(err, oidc.ErrSignupDisabled) {
		t.Fatalf("UNVERIFIED bootstrap email: %v, want ErrSignupDisabled", err)
	}
}

// While a bootstrap admin email is configured, the empty-install
// exception does not apply: only that verified address may create the
// first account.
func TestSignupGate_BootstrapEmailSetMeansNoEmptyInstallException(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "true")
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "owner@example.com")
	guard := signupgate.NewCapGuard(pool, func() int { return 100 })

	if err := guard(ctx, "squatter@example.com", "squatter@example.com", true); !errors.Is(err, oidc.ErrSignupDisabled) {
		t.Fatalf("squatter on empty install: %v, want ErrSignupDisabled", err)
	}
	if err := guard(ctx, "owner-key", "owner@example.com", false); !errors.Is(err, oidc.ErrSignupDisabled) {
		t.Fatalf("unverified owner email: %v, want ErrSignupDisabled", err)
	}
	if n, _ := db.CountUsers(ctx, pool); n != 0 {
		t.Fatalf("users = %d, refusals must not create rows", n)
	}
	if err := guard(ctx, "owner@example.com", "owner@example.com", true); err != nil {
		t.Fatalf("verified owner: %v, want nil", err)
	}
}

// Two first logins at the same instant on an empty install: exactly one
// is let in.
func TestSignupGate_ConcurrentFirstLoginsOnlyOneWins(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "true")
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "")
	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := fmt.Sprintf("race-%d@example.com", i)
			ok, err := signupgate.Allow(ctx, pool, u, u, true, "")
			if err != nil {
				t.Errorf("allow %d: %v", i, err)
			}
			results[i] = ok
		}(i)
	}
	wg.Wait()
	won := 0
	for _, ok := range results {
		if ok {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent first logins passed, want exactly 1", won)
	}
	if c, _ := db.CountUsers(ctx, pool); c != 1 {
		t.Fatalf("users = %d, want 1", c)
	}
}

// Admin promotion from RIVOLT_BOOTSTRAP_ADMIN_EMAIL needs a verified
// email; callers that can't vouch for it (EnsureUserFull) never
// promote.
func TestEnsureUser_BootstrapPromotionRequiresVerifiedEmail(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "owner@example.com")
	// A filler user so the first-row trigger can't promote anyone.
	if _, err := db.EnsureUser(ctx, pool, "filler@example.com"); err != nil {
		t.Fatal(err)
	}
	roleOf := func(username string) string {
		var role string
		if err := pool.QueryRowContext(ctx, `SELECT role FROM users WHERE username = $1`, username).Scan(&role); err != nil {
			t.Fatal(err)
		}
		return role
	}
	uid, err := db.EnsureUserVerified(ctx, pool, "owner@example.com", "owner@example.com", "", false)
	if err != nil || uid.String() == "" {
		t.Fatal(err)
	}
	if got := roleOf("owner@example.com"); got != "user" {
		t.Fatalf("unverified bootstrap email promoted: role = %s", got)
	}
	if _, err := db.EnsureUserFull(ctx, pool, "owner@example.com", "owner@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if got := roleOf("owner@example.com"); got != "user" {
		t.Fatalf("EnsureUserFull promoted: role = %s", got)
	}
	if _, err := db.EnsureUserVerified(ctx, pool, "owner@example.com", "Owner@Example.com ", "", true); err != nil {
		t.Fatal(err)
	}
	if got := roleOf("owner@example.com"); got != "admin" {
		t.Fatalf("verified bootstrap email not promoted: role = %s", got)
	}
}
