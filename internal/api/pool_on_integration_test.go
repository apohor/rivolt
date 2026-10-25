//go:build integration

package api

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
	"github.com/apohor/rivolt/internal/trips"
)

// requestPoolTests is RIVOLT_TEST_REQUEST_POOL: CI sets it so these integration tests run the way the server will with row-level
// security on (RIVOLT_REQUEST_DATABASE_URL), and a handler that reads across users, or on a context with no user, fails here
// instead of quietly returning less. Unset, they run on the one owner pool as before: CI keeps a small run that way for the
// deployments (self-hosted) that never set the request pool.
func requestPoolTests() bool { return os.Getenv("RIVOLT_TEST_REQUEST_POOL") != "" }

// poolOn returns d with its request-side stores on a real request pool (a rivolt_app-style role with the request role's grants, so
// the tenant policies apply), and the owner pool as the system side. d.DB must be the owner pool the test seeded through. With
// RIVOLT_TEST_REQUEST_POOL unset it returns d unchanged. Only the stores d already has are moved; auth and session stores a test
// builds on the owner pool stay there, as they do in main (the sign-in paths are system-pool by policy).
func poolOn(t *testing.T, d Deps) Deps {
	t.Helper()
	if !requestPoolTests() || d.DB == nil {
		return d
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	owner := d.DB

	var dbName string
	if err := owner.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatalf("poolOn: current_database: %v", err)
	}
	role := "rivolt_app_t" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := owner.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD 'pw'`); err != nil {
		t.Fatalf("poolOn: create role: %v", err)
	}
	t.Cleanup(func() { // roles are cluster-wide; the test database is dropped after this cleanup
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = owner.ExecContext(c, `DROP OWNED BY `+role)
		_, _ = owner.ExecContext(c, `DROP ROLE IF EXISTS `+role)
	})
	if err := db.PrepareRequestRole(ctx, owner, role); err != nil {
		t.Fatalf("poolOn: grants: %v", err)
	}
	u, err := url.Parse(testpg.DSNForDatabase(dbName))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "pw")
	rp, err := db.OpenRequestPool(ctx, u.String(), 8, auth.UserFromContext)
	if err != nil {
		t.Fatalf("poolOn: request pool: %v", err)
	}
	t.Cleanup(rp.Close)

	// The system side first (the originals), then the request side on the new pool.
	d.SystemDB, d.SystemSettings, d.SystemSecrets = owner, d.Settings, d.Secrets
	d.DB = rp.DB
	rf := db.NewVehicleResolverFactory(rp.DB)
	if d.Settings != nil {
		d.Settings = settings.NewFactory(rp.DB)
	}
	if d.Drives != nil {
		d.Drives = drives.NewFactory(rp.DB, rf)
	}
	if d.Charges != nil {
		d.Charges = charges.NewFactory(rp.DB, rf)
	}
	if d.Samples != nil {
		d.Samples = samples.NewFactoryWithSystem(rp.DB, owner, rf)
	}
	if d.Push != nil {
		d.Push = push.NewFactory(rp.DB)
	}
	if d.Trips != nil {
		d.Trips = trips.NewFactory(rp.DB)
	}
	d.RequestPoolOn = true
	return d
}
