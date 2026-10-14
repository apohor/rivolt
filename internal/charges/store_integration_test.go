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
	"database/sql"
	"fmt"
	"os"
	"strings"
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
		// Back-to-back, not overlapping: migration 0041 rejects two live
		// sessions for one vehicle at the same time.
		drv.EndedAt = drv.StartedAt.Add(30 * time.Second)
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

// TestMarkFree pins the explicit-free contract: cost/price stored as 0
// (not NULL) with cost_source=user, survives the recorder re-saving the
// session, is user-scoped, and UpdatePricing(0) still clears instead.
func TestMarkFree(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	ctx := context.Background()
	a := factory.For(uidA)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	c := sampleCharge("c-free", "rv-A", start, "charging_active")
	c.CostSource, c.Network = "network_estimate", "Electrify America"
	if err := a.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if n, err := factory.For(uidB).MarkFree(ctx, "c-free", "USD"); err != nil || n != 0 {
		t.Fatalf("other user's MarkFree = %d, %v; want 0 rows", n, err)
	}
	if n, err := a.MarkFree(ctx, "c-free", "USD"); err != nil || n != 1 {
		t.Fatalf("MarkFree = %d, %v", n, err)
	}
	// Recorder re-saves the still-open session with its own price.
	if err := a.Upsert(ctx, c); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	got, err := a.ListRecent(ctx, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListRecent = %v, %v", got, err)
	}
	if g := got[0]; g.Cost != 0 || g.PricePerKWh != 0 || g.CostSource != "user" || g.Network != "" || g.Currency != "USD" {
		t.Errorf("after MarkFree + re-upsert: cost %v ppk %v source %q network %q currency %q",
			g.Cost, g.PricePerKWh, g.CostSource, g.Network, g.Currency)
	}

	// Zero via UpdatePricing still means "clear": NULL cost, no source.
	if _, err := a.UpdatePricing(ctx, "c-free", 0, "", 0); err != nil {
		t.Fatalf("UpdatePricing: %v", err)
	}
	got, _ = a.ListRecent(ctx, 10)
	if got[0].CostSource != "" {
		t.Errorf("UpdatePricing(0) must clear cost_source, got %q", got[0].CostSource)
	}
}

// TestLiveOverlapGuard pins migrations 0041/0043: the database refuses
// a second live charge or drive overlapping an existing one for the same
// vehicle (SQLSTATE 23P01) at any date, while back-to-back sessions,
// imports and other vehicles are unaffected.
func TestLiveOverlapGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "overlap-guard")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	isExcl := func(err error) bool { return err != nil && strings.Contains(err.Error(), "23P01") }

	start := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	first := sampleCharge("live-1", "rv-A", start, "charging_complete") // 30 min
	if err := store.Upsert(ctx, first); err != nil {
		t.Fatalf("first live charge: %v", err)
	}
	// Second recorder, same session 3 s later: rejected.
	dup := sampleCharge("live-2", "rv-A", start.Add(3*time.Second), "charging_complete")
	if err := store.Upsert(ctx, dup); !isExcl(err) {
		t.Fatalf("overlapping live charge: err = %v, want exclusion violation 23P01", err)
	}
	// Back-to-back (starts exactly at the previous end): allowed.
	if err := store.Upsert(ctx, sampleCharge("live-3", "rv-A", first.EndedAt, "charging_complete")); err != nil {
		t.Errorf("back-to-back live charge rejected: %v", err)
	}
	// Same time on another vehicle, or from an import: allowed.
	if err := store.Upsert(ctx, sampleCharge("live-other", "rv-B", start, "charging_complete")); err != nil {
		t.Errorf("other vehicle rejected: %v", err)
	}
	imp := sampleCharge("electrafi-1", "rv-A", start.Add(time.Minute), "Complete")
	imp.Source = "electrafi_import"
	if err := store.Upsert(ctx, imp); err != nil {
		t.Errorf("overlapping import rejected: %v", err)
	}
	// Extending an existing row into the next session is rejected too.
	grown := first
	grown.EndedAt = first.EndedAt.Add(5 * time.Minute)
	if err := store.Upsert(ctx, grown); !isExcl(err) {
		t.Errorf("extending live-1 over live-3: err = %v, want 23P01", err)
	}
	// 0043 dropped 0041's 2026-05-10 cutoff: old duplicates are refused too.
	old := time.Date(2026, 5, 8, 22, 54, 46, 0, time.UTC)
	if err := store.Upsert(ctx, sampleCharge("may-a", "rv-A", old, "charging_complete")); err != nil {
		t.Fatalf("May 2026 charge: %v", err)
	}
	if err := store.Upsert(ctx, sampleCharge("may-b", "rv-A", old.Add(3*time.Second), "charging_complete")); !isExcl(err) {
		t.Errorf("May 2026 duplicate: err = %v, want 23P01 (no cutoff since 0043)", err)
	}
	var cutoffs int
	if err := pool.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint
		 WHERE conname IN ('charges_live_no_overlap', 'drives_live_no_overlap')
		   AND pg_get_constraintdef(oid) LIKE '%2026-05-10%'`).Scan(&cutoffs); err != nil || cutoffs != 0 {
		t.Errorf("constraints still carrying the 2026-05-10 cutoff = %d (%v), want 0", cutoffs, err)
	}

	// Drives carry the same guard.
	var vid string
	if err := pool.QueryRowContext(ctx, `SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = 'rv-A'`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, source) VALUES ($1, $2, $3, $4, $5, 'live')`
	if _, err := pool.ExecContext(ctx, ins, uid, vid, "d-1", start, start.Add(time.Hour)); err != nil {
		t.Fatalf("first live drive: %v", err)
	}
	if _, err := pool.ExecContext(ctx, ins, uid, vid, "d-2", start.Add(time.Minute), start.Add(2*time.Hour)); !isExcl(err) {
		t.Errorf("overlapping live drive: err = %v, want 23P01", err)
	}
}

// TestPlaceRoundTrip pins migration 0044's column: the recorder's place
// is stored, and a later write that doesn't know it keeps it.
func TestPlaceRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "place-test")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	c := sampleCharge("p-1", "rv-A", time.Now().UTC().Add(-2*time.Hour).Truncate(time.Second), "charging_active")
	c.Place = "home"
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	c.Place = "" // e.g. home lookup failed on a later frame
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	var place string
	if err := pool.QueryRowContext(ctx, `SELECT COALESCE(place, '') FROM charges WHERE external_id = 'p-1'`).Scan(&place); err != nil || place != "home" {
		t.Errorf("place = %q (%v), want home kept", place, err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET place = 'garage' WHERE external_id = 'p-1'`); err == nil {
		t.Error("place accepted a value other than home/away")
	}
}

// User prices end the reprice undo window (0040's undo selects on
// legacy_cost IS NOT NULL), and a Free row written through Upsert, as a
// backup restore does, keeps cost 0 instead of becoming "no price".
func TestUserPriceClearsLegacyAndFreeSurvivesUpsert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "legacy-test")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	start := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	for i, id := range []string{"repriced-free", "repriced-edit"} {
		c := sampleCharge(id, "rv-A", start.Add(time.Duration(i)*time.Hour), "charging_complete")
		c.CostSource = "network_estimate"
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
		// What `rivolt reprice --apply` leaves behind.
		if _, err := pool.ExecContext(ctx, `UPDATE charges SET legacy_cost = 5.5, legacy_price_per_kwh = 0.11 WHERE external_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.MarkFree(ctx, "repriced-free", "USD"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePricing(ctx, "repriced-edit", 12.5, "USD", 0.25); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE legacy_cost IS NOT NULL`).Scan(&left); err != nil || left != 0 {
		t.Errorf("rows still in the reprice undo set = %d (%v), want 0", left, err)
	}

	restored := sampleCharge("restored-free", "rv-A", start.Add(3*time.Hour), "charging_complete")
	restored.Cost, restored.PricePerKWh, restored.CostSource = 0, 0, "user"
	if err := store.Upsert(ctx, restored); err != nil {
		t.Fatal(err)
	}
	var cost, ppk *float64
	if err := pool.QueryRowContext(ctx, `SELECT cost::float8, price_per_kwh::float8 FROM charges WHERE external_id = 'restored-free'`).Scan(&cost, &ppk); err != nil {
		t.Fatal(err)
	}
	if cost == nil || *cost != 0 || ppk == nil || *ppk != 0 {
		t.Errorf("restored Free row: cost %v ppk %v, want 0 / 0 (not NULL)", cost, ppk)
	}
}

// TestLiveOverlap pins the lookup a rejected recorder uses to write only
// the tail of its session after another recorder's overlapping row.
func TestLiveOverlap(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	ghost := sampleCharge("ghost", "rv-A", start, "charging_active") // [start, start+30m], 50 kWh, 80%
	if err := store.Upsert(ctx, ghost); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	ov, err := store.LiveOverlap(ctx, "rv-A", "mine", start.Add(time.Second), start.Add(time.Hour))
	if err != nil || ov == nil {
		t.Fatalf("LiveOverlap = %v, %v; want the ghost row", ov, err)
	}
	if !ov.EndedAt.Equal(ghost.EndedAt) || ov.EndSoCPct != 80 || ov.EnergyKWh != 50 {
		t.Errorf("overlap = %+v", ov)
	}
	if ov, _ := store.LiveOverlap(ctx, "rv-A", "ghost", start, start.Add(time.Hour)); ov != nil {
		t.Errorf("own row must be excluded, got %+v", ov)
	}
	if ov, _ := store.LiveOverlap(ctx, "rv-A", "mine", ghost.EndedAt, ghost.EndedAt.Add(time.Hour)); ov != nil {
		t.Errorf("a range starting at the ghost's end doesn't overlap, got %+v", ov)
	}
}

// TestRestampPlace pins A15's correction: only place changes, user-priced
// and position-less rows are left alone, and a dry run writes nothing.
func TestRestampPlace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "restamp-test")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	// sampleCharge sits at 30.27,-97.74; "away" at 35.47,-97.52.
	mk := func(id string, i int, lat, lon float64, place, source string) {
		c := sampleCharge(id, "rv-A", start.Add(time.Duration(i)*time.Hour), "charging_complete")
		c.Lat, c.Lon, c.Place, c.CostSource = lat, lon, place, source
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	mk("was-away", 0, 30.27, -97.74, "away", "home_rate")  // at the corrected home
	mk("no-place", 1, 30.27, -97.74, "", "")               // legacy, NULL place
	mk("really-away", 2, 35.47, -97.52, "away", "")        // stays away
	mk("user-priced", 3, 30.27, -97.74, "away", "user")    // never touched
	mk("no-position", 4, 0, 0, "away", "")                 // never touched
	placeOf := func(id string) string {
		var p sql.NullString
		if err := pool.QueryRowContext(ctx, `SELECT place FROM charges WHERE external_id = $1`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p.String
	}
	var costBefore float64
	_ = pool.QueryRowContext(ctx, `SELECT sum(cost)::float8 FROM charges WHERE user_id = $1`, uid).Scan(&costBefore)

	dry, err := store.RestampPlace(ctx, 30.27, -97.74, 250, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dry.DryRun || dry.Changed != 2 || dry.ToHome != 2 || dry.ToAway != 0 || dry.Unchanged != 1 || dry.SkippedUserPriced != 1 {
		t.Errorf("dry run = %+v", dry)
	}
	if placeOf("was-away") != "away" {
		t.Error("dry run wrote")
	}

	res, err := store.RestampPlace(ctx, 30.27, -97.74, 250, false)
	if err != nil || res.Changed != 2 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	for id, want := range map[string]string{"was-away": "home", "no-place": "home", "really-away": "away",
		"user-priced": "away", "no-position": "away"} {
		if got := placeOf(id); got != want {
			t.Errorf("%s place = %q, want %q", id, got, want)
		}
	}
	var costAfter float64
	_ = pool.QueryRowContext(ctx, `SELECT sum(cost)::float8 FROM charges WHERE user_id = $1`, uid).Scan(&costAfter)
	if costAfter != costBefore {
		t.Errorf("costs changed: %v → %v", costBefore, costAfter)
	}
}

// A user-priced row with a NULL cost (left by the pre-#93 restore bug)
// is not Free: the list reports it with no cost source, as unpriced,
// matching how the ledger reads it.
func TestListRecent_UserNullCostIsNotFree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "user-null-cost")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	if err := store.Upsert(ctx, sampleCharge("broken", "rv-A", start, "charging_complete")); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, sampleCharge("free", "rv-A", start.Add(time.Hour), "charging_complete")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET cost = NULL, price_per_kwh = NULL, cost_source = 'user' WHERE external_id = 'broken'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkFree(ctx, "free", ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	src := map[string]string{}
	for _, c := range got {
		src[c.ID] = c.CostSource
	}
	if src["broken"] != "" || src["free"] != "user" {
		t.Errorf("cost sources = %v, want broken: \"\" (unpriced), free: user", src)
	}
}
