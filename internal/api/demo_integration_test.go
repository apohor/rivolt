//go:build integration

// Demo seed against real Postgres with every migration applied.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
)

func TestDemoSeed_IdempotentNonAdminAndScoped(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	sealer, err := crypto.NewEnvSealer("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	deps := demo.Deps{
		DB:       pool,
		Settings: settings.NewFactory(pool),
		Charges:  charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)),
		Secrets:  secrets.New(pool, sealer),
	}
	// The demo user is the FIRST user on an empty database: the role
	// trigger would make it admin. Seed must demote it.
	for i := 0; i < 2; i++ {
		if err := demo.Seed(ctx, deps); err != nil {
			t.Fatalf("seed #%d: %v", i+1, err)
		}
	}
	uid := demo.UserID()
	role, err := db.RoleFor(ctx, pool, uid)
	if err != nil || role != "user" {
		t.Fatalf("role = %q (%v), want user", role, err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id = $1 AND source = 'seed'`, uid).Scan(&n); err != nil || n != 4 {
		t.Fatalf("seeded charges = %d (%v), want 4 after two seeds", n, err)
	}
	home, err := settings.GetHomeLocation(ctx, deps.Settings.For(uid))
	if err != nil || !home.Set || home.Latitude != demo.HomeLat {
		t.Fatalf("home = %+v (%v)", home, err)
	}
	cc, err := settings.GetChargingConfig(ctx, deps.Settings.For(uid))
	if err != nil || cc.HomePricePerKWh != demo.HomeRate {
		t.Fatalf("rate = %v (%v)", cc.HomePricePerKWh, err)
	}
	// Another user sees none of it.
	other, err := db.EnsureUser(ctx, pool, "demo-bystander")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id = $1`, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("bystander charges = %d (%v)", n, err)
	}
	// With the env off, Seed is a no-op.
	t.Setenv("RIVOLT_DEMO", "")
	if err := demo.Seed(ctx, demo.Deps{}); err != nil {
		t.Fatalf("disabled seed: %v", err)
	}
}
