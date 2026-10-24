//go:build integration

// Demo seed against real Postgres with every migration applied.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/drives"
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
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id = $1 AND source = 'seed'`, uid).Scan(&n); err != nil || n != 2 {
		t.Fatalf("seeded charges = %d (%v), want 2 after two seeds", n, err)
	}
	// The hand-written billed and user-priced seeds are gone (the corpus
	// supplies both); a previously seeded row is removed on reseed.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source)
		SELECT $1, vehicle_id, 'demo_seed_user', started_at - interval '30 days', ended_at - interval '30 days', 'seed'
		  FROM charges WHERE user_id = $1 LIMIT 1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := demo.Seed(ctx, deps); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id = $1 AND external_id IN ('demo_seed_user','demo_seed_billed')`, uid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("dropped seeds present = %d (%v)", n, err)
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

// The public demo also runs on rivolt.dev, against prod's real users' rows: the only thing keeping an anonymous
// /api/charges/{id} and /api/drives/{id} to the demo account there is the store's user filter under the demo identity. So
// prove it end to end through the real router: another user's charge and drive, with ids of exactly the shape the demo
// allowlist admits, are a 404 for an anonymous visitor and leak nothing, while the demo account's own are served.
func TestDemoAnonymousCannotReadAnotherUsersChargeOrDrive(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	bob, err := db.EnsureUser(ctx, pool, "demo-xuser-bob")
	if err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	chargeFactory, driveFactory := charges.NewFactory(pool, resolvers), drives.NewFactory(pool, resolvers)
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	const bobChargeID, bobDriveID = "live_veh-bob-secret_c_1798000000", "live_veh-bob-secret_d_1798000000"
	demoChargeID, demoDriveID := "live_"+demo.VehicleID()+"_c_1798000001", "live_"+demo.VehicleID()+"_d_1798000001"
	for _, c := range []struct {
		uid         uuid.UUID
		id, vehicle string
	}{{bob, bobChargeID, "veh-bob-secret"}, {demoUID, demoChargeID, demo.VehicleID()}} {
		if err := chargeFactory.For(c.uid).Upsert(ctx, charges.Charge{ID: c.id, VehicleID: c.vehicle, StartedAt: start, EndedAt: start.Add(time.Hour),
			StartSoCPct: 20, EndSoCPct: 60, EnergyAddedKWh: 31.4159, MaxPowerKW: 7.4, FinalState: "charging_complete", Source: "live",
			Lat: 30.123456, Lon: -97.654321}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []struct {
		uid         uuid.UUID
		id, vehicle string
	}{{bob, bobDriveID, "veh-bob-secret"}, {demoUID, demoDriveID, demo.VehicleID()}} {
		if err := driveFactory.For(d.uid).Upsert(ctx, drives.Drive{ID: d.id, VehicleID: d.vehicle, StartedAt: start, EndedAt: start.Add(30 * time.Minute),
			StartSoCPct: 70, EndSoCPct: 60, DistanceMi: 27.1828, StartLat: 30.123456, StartLon: -97.654321, EndLat: 30.2, EndLon: -97.7, Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	if !demoAllowed("GET", "/api/charges/"+bobChargeID, demo.VehicleID()) || !demoAllowed("GET", "/api/drives/"+bobDriveID, demo.VehicleID()) {
		t.Fatal("the test's ids are not of the shape the demo allowlist admits, so it proves nothing")
	}

	authSvc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: authSvc, AuthEnforced: true, DB: pool, Drives: driveFactory, Charges: chargeFactory, Settings: settings.NewFactory(pool)})
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	// The demo account's own charge and drive are served to the anonymous visitor (marked as the demo)...
	for _, p := range []string{"/api/charges/" + demoChargeID, "/api/drives/" + demoDriveID} {
		if rec := get(p); rec.Code != http.StatusOK || rec.Header().Get(demoHeader) == "" {
			t.Fatalf("GET %s = %d (demo header %q), want 200 as the demo", p, rec.Code, rec.Header().Get(demoHeader))
		}
	}
	// ...and another user's, with ids of the same shape, are a 404 that carries nothing of theirs.
	for _, p := range []string{"/api/charges/" + bobChargeID, "/api/drives/" + bobDriveID} {
		rec := get(p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("anonymous GET %s = %d, want 404", p, rec.Code)
		}
		body := rec.Body.String()
		for _, leak := range []string{"veh-bob-secret", "31.4159", "27.1828", "30.123456", "-97.654321", "bob", "charging_complete"} {
			if strings.Contains(body, leak) {
				t.Errorf("GET %s leaked %q: %s", p, leak, body)
			}
		}
	}
	// The same ids with a sub-path the demo does not open stay closed.
	for _, p := range []string{"/api/charges/" + bobChargeID + "/place", "/api/charges/to-confirm"} {
		if rec := get(p); rec.Header().Get(demoHeader) != "" || (rec.Code >= 200 && rec.Code < 300) {
			t.Errorf("GET %s = %d (demo header %q): reachable anonymously", p, rec.Code, rec.Header().Get(demoHeader))
		}
	}
}
