//go:build integration

// Demo seed against real Postgres with every migration applied.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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

// Another user's drive id through the demo identity yields no data.
func TestDemoPerDriveWeather_ScopedToDemoUser(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	demoUID, err := db.EnsureUserFull(ctx, pool, demo.Username, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "demo-detail-bob")
	if err != nil {
		t.Fatal(err)
	}
	for uid, drive := range map[uuid.UUID]string{demoUID: "drive-demo", bob: "drive-bob"} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c)
			VALUES ($1, $2, 34.1, -118.1, now(), 'test', 20)`, uid, drive); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) int {
		r := chi.NewRouter()
		r.Get("/api/drives/{id}/weather", handleDriveWeatherGet(pool, demoUID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/drives/"+id+"/weather", nil))
		return rec.Code
	}
	if got := get("drive-demo"); got != http.StatusOK {
		t.Fatalf("demo's own drive = %d, want 200", got)
	}
	if got := get("drive-bob"); got != http.StatusNotFound {
		t.Fatalf("another user's drive through the demo identity = %d, want 404", got)
	}
}
