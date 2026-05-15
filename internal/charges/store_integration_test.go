//go:build integration

// Charges store integration tests — focus on the bug-prone surfaces:
//   1. ThermalKWh round-trip (*float64; NULL ↔ nil ↔ *0.0 are three
//      different things, and a Row.Scan-style regression here would
//      silently lose Parallax's thermal accounting).
//   2. The open-live whitelist on LatestOpenLive — the past absorber
//      bug (a brief charging_user_stopped frame between two physical
//      sessions reattached the next session to the closed row) lives
//      here and only here. A test that pins the whitelist is what
//      keeps that incident from coming back.
//   3. Cross-user isolation: every read scoped by user_id.
//
// Run with: go test -tags=integration ./internal/charges/...
package charges_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

func pgDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "rivolt",
			"POSTGRES_PASSWORD": "rivolt",
			"POSTGRES_DB":       "rivolt_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port())
}

func setupEnv(t *testing.T) (*charges.Factory, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uidA, err := db.EnsureUser(ctx, pool, "chargestest-a")
	if err != nil {
		t.Fatalf("EnsureUser A: %v", err)
	}
	uidB, err := db.EnsureUser(ctx, pool, "chargestest-b")
	if err != nil {
		t.Fatalf("EnsureUser B: %v", err)
	}
	return charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)), uidA, uidB
}

func f64p(v float64) *float64 { return &v }

func sampleCharge(id, vid string, start time.Time, state string) charges.Charge {
	return charges.Charge{
		ID: id, VehicleID: vid,
		StartedAt: start, EndedAt: start.Add(30 * time.Minute),
		StartSoCPct: 20, EndSoCPct: 80,
		EnergyAddedKWh: 50, MilesAdded: 180,
		MaxPowerKW: 200, AvgPowerKW: 120,
		FinalState: state,
		Lat:        30.27, Lon: -97.74,
		Source:      "live",
		Cost:        18.5,
		Currency:    "USD",
		PricePerKWh: 0.37,
		ThermalKWh:  f64p(2.1),
	}
}

// TestUpsertAndList_ThermalRoundTrip pins thermal_kwh handling.
// nullableFloatPtr/floatFromNull form a NULL ↔ nil ↔ *0.0 contract;
// each must round-trip distinctly or the UI's "—" vs "0 kWh" rendering
// silently misreports legacy rows.
func TestUpsertAndList_ThermalRoundTrip(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	// Row with thermal=2.1 — must read back as *2.1.
	if err := store.Upsert(ctx, sampleCharge("c-1", "rv-A", start, "charging_complete")); err != nil {
		t.Fatalf("Upsert with thermal: %v", err)
	}
	// Row with thermal=nil (legacy / non-Parallax source) — must read back nil.
	legacy := sampleCharge("c-2", "rv-A", start.Add(time.Hour), "charging_complete")
	legacy.ThermalKWh = nil
	if err := store.Upsert(ctx, legacy); err != nil {
		t.Fatalf("Upsert legacy: %v", err)
	}
	// Row with thermal=*0 — explicit zero, must read back *0.
	zero := sampleCharge("c-3", "rv-A", start.Add(2*time.Hour), "charging_complete")
	zero.ThermalKWh = f64p(0)
	if err := store.Upsert(ctx, zero); err != nil {
		t.Fatalf("Upsert zero: %v", err)
	}

	got, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	byID := map[string]charges.Charge{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if v := byID["c-1"].ThermalKWh; v == nil || *v != 2.1 {
		t.Errorf("c-1 thermal: got %v want *2.1", v)
	}
	if v := byID["c-2"].ThermalKWh; v != nil {
		t.Errorf("c-2 thermal: got %v want nil (legacy)", *v)
	}
	if v := byID["c-3"].ThermalKWh; v == nil || *v != 0 {
		t.Errorf("c-3 thermal: got %v want *0 (explicit zero)", v)
	}

	// Re-upsert c-2 from a path that doesn't track thermal — the
	// existing row's thermal_kwh stays NULL. Re-upsert c-1 from the
	// same path — the COALESCE in Upsert must NOT erase the 2.1.
	reimport := sampleCharge("c-1", "rv-A", start, "charging_complete")
	reimport.ThermalKWh = nil
	reimport.Source = "electrafi"
	if err := store.Upsert(ctx, reimport); err != nil {
		t.Fatalf("re-upsert c-1: %v", err)
	}
	got, _ = store.ListRecent(ctx, 10)
	for _, c := range got {
		if c.ID == "c-1" {
			if c.ThermalKWh == nil || *c.ThermalKWh != 2.1 {
				t.Errorf("c-1 thermal clobbered by reimport: %v", c.ThermalKWh)
			}
		}
	}
}

// TestLatestOpenLive_Whitelist is the post-incident regression for
// the absorber bug. A brief charging_user_stopped row between two
// physical sessions must NOT be returned by LatestOpenLive — that's
// what reattached the next charging_active frame to the closed row
// in the past. Only the four states in openLiveFinalStates count as
// "still open".
func TestLatestOpenLive_Whitelist(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)

	cases := []struct {
		state  string
		isOpen bool
	}{
		{"charging_active", true},
		{"charging_ready", true},
		{"charging_connecting", true},
		{"waiting_on_charger", true},
		{"charging_user_stopped", false},
		{"charging_station_stopped", false},
		{"charging_complete", false},
		{"abandoned", false},
		{"", false},
	}
	for i, c := range cases {
		drv := sampleCharge(fmt.Sprintf("c-%d", i), "rv-A", start.Add(time.Duration(i)*time.Minute), c.state)
		if err := store.Upsert(ctx, drv); err != nil {
			t.Fatalf("Upsert %q: %v", c.state, err)
		}
	}

	// Newest first; the latest open row is the most recently-started
	// row whose state is in the whitelist — that's i=3 ("waiting_on_charger").
	got, err := store.LatestOpenLive(ctx, "rv-A")
	if err != nil {
		t.Fatalf("LatestOpenLive: %v", err)
	}
	if got == nil {
		t.Fatal("LatestOpenLive returned nil")
	}
	if got.FinalState != "waiting_on_charger" {
		t.Errorf("got FinalState=%q want waiting_on_charger (whitelist broken — absorber bug coming back)", got.FinalState)
	}
}

// TestCrossUserIsolation: same architecture invariant as drives.
// charges.Factory is shared; per-user reads must stay disjoint.
func TestCrossUserIsolation(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	storeA := factory.For(uidA)
	storeB := factory.For(uidB)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	if err := storeA.Upsert(ctx, sampleCharge("a-1", "rv-A", start, "charging_complete")); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := storeB.Upsert(ctx, sampleCharge("b-1", "rv-B", start, "charging_complete")); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}
	gotA, _ := storeA.ListRecent(ctx, 100)
	gotB, _ := storeB.ListRecent(ctx, 100)
	if len(gotA) != 1 || gotA[0].ID != "a-1" {
		t.Errorf("user A leaked B's rows: %+v", gotA)
	}
	if len(gotB) != 1 || gotB[0].ID != "b-1" {
		t.Errorf("user B leaked A's rows: %+v", gotB)
	}

	// DeleteByExternalID is user-scoped — A trying to delete B's
	// row by external id must affect 0 rows.
	n, err := storeA.DeleteByExternalID(ctx, "b-1")
	if err != nil {
		t.Fatalf("DeleteByExternalID cross-user: %v", err)
	}
	if n != 0 {
		t.Errorf("cross-user delete affected %d rows — user_id scoping broken", n)
	}
	// B's row still there.
	if got, _ := storeB.Count(ctx); got != 1 {
		t.Errorf("B's row vanished after A's cross-user delete: count=%d", got)
	}
}

// TestUpdatePricing exercises the editable-cost path with a NULL
// clear: passing cost=0 must write SQL NULL so the UI falls back to
// home-rate inference instead of showing $0.00.
func TestUpdatePricing(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	c := sampleCharge("c-1", "rv-A", start, "charging_complete")
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	n, err := store.UpdatePricing(ctx, "c-1", 0, "", 0)
	if err != nil {
		t.Fatalf("UpdatePricing clear: %v", err)
	}
	if n != 1 {
		t.Fatalf("UpdatePricing affected %d rows, want 1", n)
	}
	got, _ := store.ListRecent(ctx, 10)
	if len(got) != 1 || got[0].Cost != 0 || got[0].Currency != "" || got[0].PricePerKWh != 0 {
		t.Errorf("clear didn't take: %+v", got)
	}
}

// TestCloseStaleOpenLiveBefore: janitor sweep abandons every open
// live row older than `before` for this user only — and leaves
// terminal-state rows untouched.
func TestCloseStaleOpenLiveBefore(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	storeA := factory.For(uidA)
	storeB := factory.For(uidB)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// User A: one stale open, one fresh open, one terminal.
	stale := sampleCharge("stale", "rv-A", now.Add(-2*time.Hour), "charging_active")
	stale.EndedAt = now.Add(-time.Hour)
	if err := storeA.Upsert(ctx, stale); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}
	fresh := sampleCharge("fresh", "rv-A", now.Add(-10*time.Minute), "charging_active")
	fresh.EndedAt = now
	if err := storeA.Upsert(ctx, fresh); err != nil {
		t.Fatalf("upsert fresh: %v", err)
	}
	terminal := sampleCharge("terminal", "rv-A", now.Add(-3*time.Hour), "charging_complete")
	terminal.EndedAt = now.Add(-2 * time.Hour)
	if err := storeA.Upsert(ctx, terminal); err != nil {
		t.Fatalf("upsert terminal: %v", err)
	}
	// User B: own stale open — must NOT be touched by A's janitor.
	bStale := sampleCharge("b-stale", "rv-B", now.Add(-2*time.Hour), "charging_active")
	bStale.EndedAt = now.Add(-time.Hour)
	if err := storeB.Upsert(ctx, bStale); err != nil {
		t.Fatalf("upsert B stale: %v", err)
	}

	n, err := storeA.CloseStaleOpenLiveBefore(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("CloseStaleOpenLiveBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("affected %d rows, want 1 (just A's stale row)", n)
	}

	got, _ := storeA.ListRecent(ctx, 100)
	states := map[string]string{}
	for _, c := range got {
		states[c.ID] = c.FinalState
	}
	if states["stale"] != "abandoned" {
		t.Errorf("stale not abandoned: %q", states["stale"])
	}
	if states["fresh"] != "charging_active" {
		t.Errorf("fresh wrongly abandoned: %q", states["fresh"])
	}
	if states["terminal"] != "charging_complete" {
		t.Errorf("terminal wrongly touched: %q", states["terminal"])
	}

	bGot, _ := storeB.ListRecent(ctx, 100)
	if len(bGot) != 1 || bGot[0].FinalState != "charging_active" {
		t.Errorf("B's stale row touched by A's janitor: %+v", bGot)
	}
}
