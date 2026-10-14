//go:build integration

// RIVOLT_SIGNUP_DISABLED first sign-in rule against real Postgres.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/signupgate"
)

func TestSignupGate_ExistingBootstrapAndEmptyInstall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "")

	// Other tests in this package share the container; make this one
	// independent of order by checking the rule relative to the table.
	n, err := db.CountUsers(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := signupgate.Allow(ctx, pool, "gate-first@example.com", "gate-first@example.com")
	if err != nil || ok != (n == 0) {
		t.Fatalf("unknown identity with %d users: allow = %v, %v; want %v", n, ok, err, n == 0)
	}
	if _, err := db.EnsureUser(ctx, pool, "gate-existing@example.com"); err != nil {
		t.Fatal(err)
	}
	// With at least one user, an unknown identity is refused...
	if ok, err := signupgate.Allow(ctx, pool, "gate-stranger@example.com", "gate-stranger@example.com"); err != nil || ok {
		t.Fatalf("unknown identity: allow = %v, %v; want false", ok, err)
	}
	// ...an existing or admin-created user still signs in...
	if ok, err := signupgate.Allow(ctx, pool, "gate-existing@example.com", "gate-existing@example.com"); err != nil || !ok {
		t.Fatalf("existing user: allow = %v, %v; want true", ok, err)
	}
	// ...and the bootstrap admin email is always let in.
	t.Setenv("RIVOLT_BOOTSTRAP_ADMIN_EMAIL", "Gate-Boss@Example.com")
	if ok, err := signupgate.Allow(ctx, pool, "gate-boss@example.com", "gate-boss@example.com"); err != nil || !ok {
		t.Fatalf("bootstrap admin: allow = %v, %v; want true", ok, err)
	}
}
