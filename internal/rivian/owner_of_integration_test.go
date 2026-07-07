//go:build integration

// Postgres-backed test for MonitorRegistry.ownerOf. Pins the tie-break
// that decides which account's session drives a vehicle's subscription
// when more than one Rivolt account owns the same physical Rivian car
// (the duplicate-account case). The naive LIMIT 1 bound the
// subscription to an arbitrary row, which parked telemetry under a
// still-needs_reauth account even though a re-authed account existed.
//
// Run with: go test -tags=integration ./internal/rivian/...
package rivian

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
)

func ownerTestPool(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "rivolt",
			"POSTGRES_PASSWORD": "rivolt",
			"POSTGRES_DB":       "rivolt_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port())
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// addOwner inserts a user (with role, needs_reauth, disabled) plus a
// vehicles row for rivianID, and optionally a rivian.session secret
// with the given age. Returns the user id.
func addOwner(ctx context.Context, t *testing.T, pool *sql.DB, name, rivianID string, needsReauth, disabled bool, session *time.Time) uuid.UUID {
	t.Helper()
	var uid uuid.UUID
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO users (id, username, email, needs_reauth, disabled)
		VALUES (gen_random_uuid(), $1, $1 || '@example.test', $2, $3) RETURNING id`,
		name, needsReauth, disabled,
	).Scan(&uid); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	if session != nil {
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO user_secrets (user_id, name, ciphertext, kek_id, updated_at)
			VALUES ($1, 'rivian.session', '\x00', 'test', $2)`, uid, *session); err != nil {
			t.Fatalf("insert session %s: %v", name, err)
		}
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2)`, uid, rivianID); err != nil {
		t.Fatalf("insert vehicle %s: %v", name, err)
	}
	return uid
}

func newOwnerRegistry(pool *sql.DB) *MonitorRegistry {
	return NewMonitorRegistry(pool, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

// TestOwnerOf_PrefersHealthyOwner is bruce's exact case: two accounts
// own one car; the healthy (re-authed, fresh session) one must win over
// the needs_reauth one regardless of insertion order.
func TestOwnerOf_PrefersHealthyOwner(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	const vin = "01-shared"

	old := time.Now().Add(-720 * time.Hour) // stale June-era session
	fresh := time.Now().Add(-1 * time.Hour) // re-authed recently
	// Insert the STUCK account first so a naive LIMIT 1 would pick it.
	stuck := addOwner(ctx, t, pool, "stuck", vin, true /*needs_reauth*/, false, &old)
	healthy := addOwner(ctx, t, pool, "healthy", vin, false, false, &fresh)

	got, err := newOwnerRegistry(pool).ownerOf(vin)
	if err != nil {
		t.Fatalf("ownerOf: %v", err)
	}
	if got != healthy {
		t.Fatalf("ownerOf = %s, want healthy owner %s (not stuck %s)", got, healthy, stuck)
	}
}

// TestOwnerOf_SingleOwnerUnaffected: the common case still returns the
// sole owner even with no stored session.
func TestOwnerOf_SingleOwnerUnaffected(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	const vin = "01-solo"
	only := addOwner(ctx, t, pool, "solo", vin, false, false, nil)

	got, err := newOwnerRegistry(pool).ownerOf(vin)
	if err != nil {
		t.Fatalf("ownerOf: %v", err)
	}
	if got != only {
		t.Fatalf("ownerOf = %s, want sole owner %s", got, only)
	}
}

// TestOwnerOf_DisabledDeprioritized: an enabled owner wins over a
// disabled one even if the disabled one has the fresher session.
func TestOwnerOf_DisabledDeprioritized(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	const vin = "01-disabled-case"

	fresh := time.Now().Add(-1 * time.Hour)
	older := time.Now().Add(-10 * time.Hour)
	disabled := addOwner(ctx, t, pool, "disabled", vin, false, true, &fresh)
	enabled := addOwner(ctx, t, pool, "enabled", vin, false, false, &older)

	got, err := newOwnerRegistry(pool).ownerOf(vin)
	if err != nil {
		t.Fatalf("ownerOf: %v", err)
	}
	if got != enabled {
		t.Fatalf("ownerOf = %s, want enabled owner %s (not disabled %s)", got, enabled, disabled)
	}
}

// TestOwnerOf_NoRowsIsErrNoRows: an unknown vehicle surfaces
// sql.ErrNoRows so EnsureSubscribed can no-op cleanly.
func TestOwnerOf_NoRowsIsErrNoRows(t *testing.T) {
	pool := ownerTestPool(t)
	if _, err := newOwnerRegistry(pool).ownerOf("01-nonexistent"); err != sql.ErrNoRows {
		t.Fatalf("ownerOf(unknown) err = %v, want sql.ErrNoRows", err)
	}
}
