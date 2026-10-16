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
	"errors"
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

// rivian_charger round-trips, and a later upsert that doesn't carry it
// (a frame before the REST fetch, a restart) keeps the stored value.
func TestRivianChargerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "rivian-charger-test")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	read := func(id string) sql.NullBool {
		var v sql.NullBool
		if err := pool.QueryRowContext(ctx, `SELECT rivian_charger FROM charges WHERE user_id = $1::uuid AND external_id = $2::text`, uid, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	c := sampleCharge("rc-1", "rv-A", time.Now().UTC().Add(-2*time.Hour).Truncate(time.Second), "charging_active")
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if v := read("rc-1"); v.Valid {
		t.Errorf("rivian_charger = %v, want NULL when not reported", v)
	}
	f := false
	c.RivianCharger = &f
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	c.RivianCharger = nil
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	if v := read("rc-1"); !v.Valid || v.Bool {
		t.Errorf("rivian_charger = %v, want false kept", v)
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

// TestRestampPlace pins A15/A15b: only place changes, except that AC
// charges becoming home and unpriced get the home rate; user-priced and
// position-less rows are left alone; a dry run writes nothing; undo puts
// it all back without removing a price the user set since.
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
	// sampleCharge: 30 min, 50 kWh, peak 200 kW (DCFC), priced, at 30.27,-97.74.
	mk := func(id string, i int, lat, lon float64, place, source string, peak float64, priced bool) {
		c := sampleCharge(id, "rv-A", start.Add(time.Duration(i)*time.Hour), "charging_complete")
		c.Lat, c.Lon, c.Place, c.CostSource, c.MaxPowerKW = lat, lon, place, source, peak
		if !priced {
			c.Cost, c.PricePerKWh, c.Currency = 0, 0, ""
		}
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	mk("was-away", 0, 30.27, -97.74, "away", "home_rate", 7.6, true)  // priced: keeps its price
	mk("unpriced-ac", 1, 30.27, -97.74, "away", "", 7.6, false)       // gets the home rate
	mk("unpriced-dcfc", 2, 30.27, -97.74, "away", "", 200, false)     // fast: stays unpriced
	mk("really-away", 3, 35.47, -97.52, "away", "", 7.6, false)       // stays away and unpriced
	mk("user-priced", 4, 30.27, -97.74, "away", "user", 7.6, true)    // never touched
	mk("no-position", 5, 0, 0, "away", "", 7.6, false)                // never touched
	mk("unpriced-ac2", 6, 30.27, -97.74, "away", "", 7.6, false)      // gets the home rate, then the user edits it
	type row struct {
		place, source string
		cost          sql.NullFloat64
	}
	get := func(id string) row {
		var r row
		var p, src sql.NullString
		if err := pool.QueryRowContext(ctx, `SELECT place, cost_source, cost::float8 FROM charges WHERE external_id = $1`, id).
			Scan(&p, &src, &r.cost); err != nil {
			t.Fatal(err)
		}
		r.place, r.source = p.String, src.String
		return r
	}
	opts := charges.RestampOptions{HomeLat: 30.27, HomeLon: -97.74, RadiusM: 250, HomeRate: 0.16, Currency: "USD",
		IsDCFC: func(peak, _, _ float64, _ time.Duration) bool { return peak >= 50 }}

	opts.DryRun = true
	dry, err := store.RestampPlace(ctx, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Changed != 4 || dry.ToHome != 4 || dry.WouldPriceAtHomeRate != 2 || dry.TotalCostDelta != 16 ||
		dry.Unchanged != 1 || dry.SkippedUserPriced != 1 || dry.RestampID != nil {
		t.Errorf("dry run = %+v", dry)
	}
	if get("unpriced-ac").place != "away" {
		t.Error("dry run wrote")
	}

	opts.DryRun = false
	res, err := store.RestampPlace(ctx, opts)
	if err != nil || res.RestampID == nil || res.Changed != 4 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if r := get("unpriced-ac"); r.place != "home" || r.source != "home_rate" || !r.cost.Valid || r.cost.Float64 != 8 {
		t.Errorf("unpriced AC after correction = %+v, want home / home_rate / 8.0", r)
	}
	if r := get("unpriced-dcfc"); r.place != "home" || r.cost.Valid {
		t.Errorf("unpriced DCFC = %+v, want home and still unpriced", r)
	}
	if r := get("was-away"); r.place != "home" || r.cost.Float64 != 18.5 {
		t.Errorf("priced row = %+v, want home with its price kept", r)
	}
	for id, want := range map[string]string{"really-away": "away", "user-priced": "away", "no-position": "away"} {
		if got := get(id).place; got != want {
			t.Errorf("%s place = %q, want %q", id, got, want)
		}
	}

	// The user re-prices one of the corrected charges by hand, then
	// undoes the correction: places revert, the home-rate price the
	// correction added goes, the user's own price stays.
	if _, err := store.UpdatePricing(ctx, "unpriced-ac2", 2.5, "USD", 0.05); err != nil {
		t.Fatal(err)
	}
	undo, err := store.UndoRestamp(ctx, nil)
	if err != nil || undo.RestampID != *res.RestampID || undo.RestoredPlace != 4 || undo.Unpriced != 1 {
		t.Fatalf("undo = %+v, %v", undo, err)
	}
	if r := get("unpriced-ac"); r.place != "away" || r.cost.Valid || r.source != "" {
		t.Errorf("unpriced AC after undo = %+v, want away and unpriced", r)
	}
	if r := get("unpriced-ac2"); r.place != "away" || r.source != "user" || r.cost.Float64 != 2.5 {
		t.Errorf("user re-priced row after undo = %+v, want away with the user's price kept", r)
	}
	if r := get("unpriced-dcfc"); r.place != "away" || r.cost.Valid {
		t.Errorf("DCFC after undo = %+v, want away and unpriced", r)
	}
	if _, err := store.UndoRestamp(ctx, nil); !errors.Is(err, charges.ErrNoRestamp) {
		t.Errorf("second undo = %v, want ErrNoRestamp", err)
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

// TestLiveCovers pins the lookup importers use to skip sessions the
// live recorder already captured (A7).
func TestLiveCovers(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	if err := store.Upsert(ctx, sampleCharge("live-1", "rv-A", start, "charging_complete")); err != nil { // 30 min
		t.Fatal(err)
	}
	imp := sampleCharge("electrafi-1", "rv-A", start.Add(2*time.Hour), "Complete")
	imp.Source = "electrafi_import"
	if err := store.Upsert(ctx, imp); err != nil {
		t.Fatal(err)
	}
	check := func(name string, s *charges.Store, vid string, from, to time.Time, want bool) {
		t.Helper()
		got, err := s.LiveCovers(ctx, vid, from, to)
		if err != nil || got != want {
			t.Errorf("%s: LiveCovers = %v, %v; want %v", name, got, err, want)
		}
	}
	check("overlaps the live row", store, "rv-A", start.Add(10*time.Minute), start.Add(time.Hour), true)
	check("starts at its end", store, "rv-A", start.Add(30*time.Minute), start.Add(time.Hour), false)
	check("only an import there", store, "rv-A", start.Add(2*time.Hour), start.Add(3*time.Hour), false)
	check("other vehicle", store, "rv-B", start, start.Add(time.Hour), false)
	check("other user", factory.For(uidB), "rv-A", start, start.Add(time.Hour), false)
}

// TestUndoRestampLatestOnly: an older correction can't be undone over a
// newer one (it would put back places the newer one changed).
func TestUndoRestampLatestOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "restamp-order")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	c := sampleCharge("order-1", "rv-A", time.Now().UTC().Add(-24*time.Hour).Truncate(time.Second), "charging_complete")
	c.Lat, c.Lon, c.Place = 30.27, -97.74, "away"
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	place := func() string {
		var p sql.NullString
		if err := pool.QueryRowContext(ctx, `SELECT place FROM charges WHERE external_id = 'order-1'`).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p.String
	}
	restamp := func(lat, lon float64) uuid.UUID {
		t.Helper()
		res, err := store.RestampPlace(ctx, charges.RestampOptions{HomeLat: lat, HomeLon: lon, RadiusM: 250,
			IsDCFC: func(float64, float64, float64, time.Duration) bool { return false }})
		if err != nil || res.RestampID == nil {
			t.Fatalf("restamp: %+v, %v", res, err)
		}
		return *res.RestampID
	}
	a := restamp(30.27, -97.74) // away → home
	b := restamp(35.47, -97.52) // home → away
	if place() != "away" {
		t.Fatalf("after both corrections: %q", place())
	}

	if _, err := store.UndoRestamp(ctx, &a); !errors.Is(err, charges.ErrRestampNotLatest) {
		t.Errorf("undo older = %v, want ErrRestampNotLatest", err)
	}
	if place() != "away" {
		t.Errorf("a refused undo changed the row: %q", place())
	}
	if _, err := store.UndoRestamp(ctx, &b); err != nil {
		t.Fatalf("undo latest by id: %v", err)
	}
	if place() != "home" {
		t.Errorf("after undoing b: %q, want home", place())
	}
	if _, err := store.UndoRestamp(ctx, ptr(uuid.New())); !errors.Is(err, charges.ErrNoRestamp) {
		t.Errorf("unknown id = %v, want ErrNoRestamp", err)
	}
	if _, err := store.UndoRestamp(ctx, &b); !errors.Is(err, charges.ErrNoRestamp) {
		t.Errorf("undone id = %v, want ErrNoRestamp", err)
	}
	if _, err := store.UndoRestamp(ctx, &a); err != nil { // now the latest
		t.Fatalf("undo a: %v", err)
	}
	if place() != "away" {
		t.Errorf("after undoing both: %q, want the original away", place())
	}
	if _, err := store.UndoRestamp(ctx, nil); !errors.Is(err, charges.ErrNoRestamp) {
		t.Errorf("nothing left = %v, want ErrNoRestamp", err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestNetworkState: auto-detection's verify state round-trips, a
// confirm sticks through the recorder re-saving the row, only an
// estimate can be confirmed, and a user's own price ends the state.
func TestNetworkState(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	est := sampleCharge("n-1", "rv-A", start, "charging_complete")
	est.CostSource, est.Network = "network_estimate", "Tesla Supercharger"
	est.NetworkState, est.NetworkConfidence = "estimated", f64p(0.9)
	est.NetworkEvidence = []byte(`{"v":1,"source":"archive","candidates":[{"network":"Tesla Supercharger","slug":"tesla_sc","dist_m":38}]}`)
	if err := store.Upsert(ctx, est); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	unmatched := sampleCharge("n-2", "rv-A", start.Add(2*time.Hour), "charging_complete")
	unmatched.Cost, unmatched.PricePerKWh, unmatched.Currency = 0, 0, ""
	unmatched.NetworkState, unmatched.NetworkConfidence = "needs_confirmation", f64p(0)
	unmatched.NetworkEvidence = []byte(`{"v":1,"source":"none","candidates":[]}`)
	if err := store.Upsert(ctx, unmatched); err != nil {
		t.Fatalf("Upsert unmatched: %v", err)
	}

	get := func(id string) charges.Charge {
		t.Helper()
		all, err := store.ListRecent(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range all {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("%s not listed", id)
		return charges.Charge{}
	}
	if c := get("n-1"); c.NetworkState != "estimated" || c.NetworkConfidence == nil || *c.NetworkConfidence < 0.89 ||
		!strings.Contains(string(c.NetworkEvidence), `"tesla_sc"`) {
		t.Fatalf("round trip: state %q conf %v evidence %s", c.NetworkState, c.NetworkConfidence, c.NetworkEvidence)
	}
	if n, a, err := store.NetworkToConfirm(ctx); err != nil || n != 2 || a != 1 {
		t.Fatalf("to confirm = %d / %d (%v), want 2 / 1", n, a, err)
	}
	if n, _, _ := factory.For(uidB).NetworkToConfirm(ctx); n != 0 {
		t.Fatalf("another user sees %d to confirm", n)
	}

	// Nothing matched: no estimate to confirm.
	if n, err := store.ConfirmNetwork(ctx, "n-2"); err != nil || n != 0 {
		t.Fatalf("confirm unmatched = %d (%v), want 0", n, err)
	}
	// Another user can't confirm it.
	if n, _ := factory.For(uidB).ConfirmNetwork(ctx, "n-1"); n != 0 {
		t.Fatalf("cross-user confirm affected %d", n)
	}
	if n, err := store.ConfirmNetwork(ctx, "n-1"); err != nil || n != 1 {
		t.Fatalf("confirm = %d (%v), want 1", n, err)
	}
	// The recorder re-saving the row (no detection on this pass, another
	// network) keeps the confirmed network and state.
	resave := est
	resave.Network, resave.NetworkState, resave.NetworkConfidence, resave.NetworkEvidence = "Electrify America", "", nil, nil
	if err := store.Upsert(ctx, resave); err != nil {
		t.Fatal(err)
	}
	if c := get("n-1"); c.NetworkState != "confirmed" || c.Network != "Tesla Supercharger" || c.NetworkConfidence == nil {
		t.Fatalf("after re-save: state %q network %q conf %v", c.NetworkState, c.Network, c.NetworkConfidence)
	}
	if n, a, _ := store.NetworkToConfirm(ctx); n != 1 || a != 1 {
		t.Fatalf("to confirm after confirm = %d / %d, want 1 / 1", n, a)
	}

	// The user's own price ends the state; the evidence stays.
	if _, err := store.UpdatePricing(ctx, "n-2", 24, "USD", 0.48); err != nil {
		t.Fatal(err)
	}
	if c := get("n-2"); c.NetworkState != "" || c.CostSource != "user" || len(c.NetworkEvidence) == 0 {
		t.Fatalf("after user price: state %q source %q evidence %s", c.NetworkState, c.CostSource, c.NetworkEvidence)
	}
	if _, err := store.MarkFree(ctx, "n-1", "USD"); err != nil {
		t.Fatal(err)
	}
	if c := get("n-1"); c.NetworkState != "" || c.CostSource != "user" {
		t.Fatalf("after free: state %q source %q", c.NetworkState, c.CostSource)
	}
	if n, _, _ := store.NetworkToConfirm(ctx); n != 0 {
		t.Fatalf("to confirm at the end = %d", n)
	}
}

// TestNearestVerifiedNetwork: site memory only counts this user's
// settled DC fast charges (confirmed, or a user price naming a network)
// within the radius, nearest first.
func TestNearestVerifiedNetwork(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	// Each charge gets its own hour: live charges of one vehicle can't
	// overlap (charges_live_no_overlap).
	slot := 0
	put := func(s *charges.Store, id string, lat, lon float64, mut func(*charges.Charge)) {
		t.Helper()
		slot++
		c := sampleCharge(id, "rv-A", start.Add(time.Duration(slot)*time.Hour), "charging_complete")
		c.Lat, c.Lon = lat, lon
		mut(&c)
		if err := s.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	const lat, lon = 30.2700, -97.7400
	// ~60 m north: confirmed Electrify America.
	put(store, "v-conf", lat+0.00054, lon, func(c *charges.Charge) {
		c.CostSource, c.Network, c.NetworkState = "network_estimate", "Electrify America", "estimated"
	})
	if n, err := store.ConfirmNetwork(ctx, "v-conf"); err != nil || n != 1 {
		t.Fatalf("confirm: %d %v", n, err)
	}
	// ~30 m: an unconfirmed estimate (not settled), and an AC session.
	put(store, "v-est", lat+0.00027, lon, func(c *charges.Charge) {
		c.CostSource, c.Network, c.NetworkState = "network_estimate", "Tesla Supercharger", "estimated"
	})
	put(store, "v-ac", lat, lon+0.0001, func(c *charges.Charge) {
		c.CostSource, c.Network, c.MaxPowerKW = "user", "ChargePoint", 7
	})
	// ~400 m: a user price naming a network, outside the radius.
	put(store, "v-far", lat+0.0036, lon, func(c *charges.Charge) { c.CostSource, c.Network = "user", "EVgo" })
	// Another user's confirmed charge right on the spot.
	put(factory.For(uidB), "v-other", lat, lon, func(c *charges.Charge) {
		c.CostSource, c.Network, c.NetworkState = "user", "IONNA", ""
	})

	got, ok, err := store.NearestVerifiedNetwork(ctx, lat, lon, 100)
	if err != nil || !ok || got.Network != "Electrify America" || got.DistM < 50 || got.DistM > 70 {
		t.Fatalf("got %+v ok=%v err=%v, want Electrify America at ~60 m", got, ok, err)
	}
	if _, ok, _ := store.NearestVerifiedNetwork(ctx, lat+0.01, lon, 100); ok {
		t.Fatal("found a verified network 1 km away")
	}
	if _, ok, _ := store.NearestVerifiedNetwork(ctx, 0, 0, 100); ok {
		t.Fatal("no position must find nothing")
	}
}

// TestNetworkStateSurvivesResaves (devils-advocate's must-fix): the
// recorder re-saving a detected row without a state keeps it; Rivian's
// bill ends it; a confirmed row keeps its price until that bill.
func TestNetworkStateSurvivesResaves(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	get := func(id string) charges.Charge {
		t.Helper()
		all, err := store.ListRecent(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range all {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("%s not listed", id)
		return charges.Charge{}
	}
	est := sampleCharge("s-1", "rv-A", start, "charging_complete")
	est.CostSource, est.Network, est.Cost, est.PricePerKWh = "network_estimate", "Tesla Supercharger", 20, 0.40
	est.NetworkState, est.NetworkConfidence, est.NetworkEvidence = "estimated", f64p(0.9), []byte(`{"v":1}`)
	if err := store.Upsert(ctx, est); err != nil {
		t.Fatal(err)
	}

	// A re-save without detection (another pass, or the plain pricer).
	resave := est
	resave.NetworkState, resave.NetworkConfidence, resave.NetworkEvidence = "", nil, nil
	if err := store.Upsert(ctx, resave); err != nil {
		t.Fatal(err)
	}
	if c := get("s-1"); c.NetworkState != "estimated" || c.NetworkConfidence == nil || len(c.NetworkEvidence) == 0 {
		t.Fatalf("re-save without a state wiped it: %q %v %s", c.NetworkState, c.NetworkConfidence, c.NetworkEvidence)
	}

	// Rivian's bill arrives: it wins and the verify state ends.
	billed := resave
	billed.CostSource, billed.Cost, billed.PricePerKWh, billed.Network = "rivian", 27, 0.54, ""
	if err := store.Upsert(ctx, billed); err != nil {
		t.Fatal(err)
	}
	if c := get("s-1"); c.CostSource != "rivian" || c.Cost != 27 || c.NetworkState != "" {
		t.Fatalf("Rivian bill: source %q cost %.2f state %q", c.CostSource, c.Cost, c.NetworkState)
	}

	// A confirmed estimate keeps its price through re-saves...
	conf := sampleCharge("s-2", "rv-A", start.Add(time.Hour), "charging_complete")
	conf.CostSource, conf.Network, conf.Cost, conf.PricePerKWh = "network_estimate", "Electrify America", 24, 0.48
	conf.NetworkState, conf.NetworkConfidence = "estimated", f64p(0.9)
	if err := store.Upsert(ctx, conf); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ConfirmNetwork(ctx, "s-2"); err != nil || n != 1 {
		t.Fatalf("confirm: %d %v", n, err)
	}
	again := conf
	again.Cost, again.PricePerKWh, again.Network, again.NetworkState = 30, 0.60, "Tesla Supercharger", "needs_confirmation"
	if err := store.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	if c := get("s-2"); c.Cost != 24 || c.PricePerKWh != 0.48 || c.Network != "Electrify America" ||
		c.CostSource != "network_estimate" || c.NetworkState != "confirmed" {
		t.Fatalf("confirmed row re-saved: cost %.2f rate %.2f network %q source %q state %q",
			c.Cost, c.PricePerKWh, c.Network, c.CostSource, c.NetworkState)
	}
	// ...until Rivian's bill.
	again.CostSource, again.Cost, again.Network = "rivian", 26, ""
	if err := store.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	if c := get("s-2"); c.CostSource != "rivian" || c.Cost != 26 || c.NetworkState != "" {
		t.Fatalf("confirmed then billed: source %q cost %.2f state %q", c.CostSource, c.Cost, c.NetworkState)
	}
}

// TestSetNetworkPriceAndGet: the user's own network choice prices the
// session (rate × energy), marks it confirmed, keeps the evidence, and
// is scoped to the user; GetByExternalID sees the state fields.
func TestSetNetworkPriceAndGet(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	c := sampleCharge("p-1", "rv-A", start, "charging_complete")
	c.EnergyAddedKWh = 40
	c.CostSource, c.Network, c.NetworkState = "network_estimate", "Tesla Supercharger", "needs_confirmation"
	c.NetworkConfidence, c.NetworkEvidence = f64p(0.5), []byte(`{"v":1}`)
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByExternalID(ctx, "p-1")
	if err != nil || got == nil || got.NetworkState != "needs_confirmation" || got.NetworkConfidence == nil || string(got.NetworkEvidence) == "" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if other, err := factory.For(uidB).GetByExternalID(ctx, "p-1"); err != nil || other != nil {
		t.Fatalf("another user got %+v (%v)", other, err)
	}
	if n, _ := factory.For(uidB).SetNetworkPrice(ctx, "p-1", "Electrify America", "USD", 0.48); n != 0 {
		t.Fatalf("cross-user set affected %d", n)
	}
	if n, err := store.SetNetworkPrice(ctx, "p-1", "Electrify America", "USD", 0.48); err != nil || n != 1 {
		t.Fatalf("set = %d (%v)", n, err)
	}
	got, _ = store.GetByExternalID(ctx, "p-1")
	if got.CostSource != "user" || got.Network != "Electrify America" || got.NetworkState != "confirmed" ||
		got.PricePerKWh != 0.48 || got.Cost < 19.19 || got.Cost > 19.21 || len(got.NetworkEvidence) == 0 {
		t.Fatalf("after set: %+v", got)
	}
}
