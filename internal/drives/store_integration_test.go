//go:build integration

// Drives store integration tests — same shape as trips: spin a real
// Postgres, exercise Upsert + ListRecent + cross-user isolation. The
// architecture invariant we're protecting is that every read is
// scoped by user_id; a future refactor that drops the WHERE clause
// would let user B see user A's drives, which is the worst-case
// multi-tenant bug. The "drives row created by user A is invisible
// to user B" assertion below catches that regression directly.
//
// Run with:
//
//	go test -tags=integration ./internal/drives/...
package drives_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/testpg"
)

func pgDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	return testpg.DSN(ctx, t)
}

// setupEnv brings up Postgres, runs migrations, seeds two users, and
// returns a shared drives.Factory plus both user IDs. The factory is
// shared because that's how main constructs it; the per-user
// isolation must hold despite the shared factory.
func setupEnv(t *testing.T) (*drives.Factory, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	dsn := pgDSN(ctx, t)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uidA, err := db.EnsureUser(ctx, pool, "drivestest-a")
	if err != nil {
		t.Fatalf("EnsureUser A: %v", err)
	}
	uidB, err := db.EnsureUser(ctx, pool, "drivestest-b")
	if err != nil {
		t.Fatalf("EnsureUser B: %v", err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	return drives.NewFactory(pool, resolvers), uidA, uidB
}

func sampleDrive(id, vid string, start time.Time) drives.Drive {
	return drives.Drive{
		ID:              id,
		VehicleID:       vid,
		StartedAt:       start,
		EndedAt:         start.Add(30 * time.Minute),
		StartSoCPct:     80,
		EndSoCPct:       70,
		StartOdometerMi: 10000,
		EndOdometerMi:   10025,
		DistanceMi:      25,
		StartLat:        30.27, StartLon: -97.74,
		EndLat: 30.5, EndLon: -97.9,
		MaxSpeedMph:   72,
		AvgSpeedMph:   50,
		EnergyUsedKWh: 8.4,
		Source:        "live",
		RoutePolyline: "abc123",
	}
}

// TestUpsertAndList covers the round-trip plus the route_polyline
// preservation contract: a second Upsert from an importer source that
// carries no polyline must NOT clobber the one stored by the live
// recorder. That's encoded in the COALESCE in Upsert; a future
// refactor of the ON CONFLICT clause would silently break it.
func TestUpsertAndList(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	if store == nil {
		t.Fatal("factory.For returned nil")
	}
	ctx := context.Background()
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := store.Upsert(ctx, sampleDrive("ext-1", "rv-vehicle-A", start)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 drive, got %d", len(got))
	}
	d := got[0]
	if d.ID != "ext-1" || d.VehicleID != "rv-vehicle-A" {
		t.Errorf("ids: %+v", d)
	}
	if d.RoutePolyline != "abc123" {
		t.Errorf("polyline: got %q want abc123", d.RoutePolyline)
	}
	if d.EnergyUsedKWh != 8.4 {
		t.Errorf("energy: got %v", d.EnergyUsedKWh)
	}

	// Re-upsert the same external_id with no polyline (the ElectraFi
	// importer shape). The COALESCE in Upsert must preserve the
	// live recorder's polyline.
	reimport := sampleDrive("ext-1", "rv-vehicle-A", start)
	reimport.RoutePolyline = ""
	reimport.Source = "electrafi"
	if err := store.Upsert(ctx, reimport); err != nil {
		t.Fatalf("Upsert reimport: %v", err)
	}
	got, err = store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent after reimport: %v", err)
	}
	if got[0].RoutePolyline != "abc123" {
		t.Errorf("polyline clobbered by importer: %q", got[0].RoutePolyline)
	}
	if got[0].Source != "electrafi" {
		t.Errorf("source not updated: %q", got[0].Source)
	}
}

// TestUpsert_NullableZeros pins the nullIfZero contract: a drive
// imported with no telemetry-derived speeds/positions writes NULLs,
// and ListRecent's COALESCE(...,0) reads them back as zeros — not
// as scan errors. A regression here would surface as List failing
// on legacy/sparse rows.
func TestUpsert_NullableZeros(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	d := drives.Drive{
		ID: "sparse-1", VehicleID: "rv-vehicle-A",
		StartedAt: start, EndedAt: start.Add(10 * time.Minute),
		Source: "electrafi",
		// every numeric field zero → NULL on write; no polyline
	}
	if err := store.Upsert(ctx, d); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].MaxSpeedMph != 0 || got[0].DistanceMi != 0 || got[0].RoutePolyline != "" {
		t.Errorf("expected zero/empty defaults, got %+v", got[0])
	}
}

// TestCrossUserIsolation is the architecture invariant from
// CLAUDE.md: every read is user-scoped. A drive written by user A
// must be invisible to user B even though both share the same pool
// and the same drives.Factory.
func TestCrossUserIsolation(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	storeA := factory.For(uidA)
	storeB := factory.For(uidB)
	if storeA == nil || storeB == nil {
		t.Fatal("factory.For returned nil for a valid user")
	}
	ctx := context.Background()
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)

	if err := storeA.Upsert(ctx, sampleDrive("a-1", "rv-A", start)); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := storeB.Upsert(ctx, sampleDrive("b-1", "rv-B", start.Add(time.Minute))); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}

	gotA, err := storeA.ListRecent(ctx, 100)
	if err != nil {
		t.Fatalf("ListRecent A: %v", err)
	}
	gotB, err := storeB.ListRecent(ctx, 100)
	if err != nil {
		t.Fatalf("ListRecent B: %v", err)
	}
	if len(gotA) != 1 || gotA[0].ID != "a-1" {
		t.Errorf("user A list: %+v", gotA)
	}
	if len(gotB) != 1 || gotB[0].ID != "b-1" {
		t.Errorf("user B list: %+v", gotB)
	}
	// Counts are user-scoped through the same path.
	if n, _ := storeA.Count(ctx); n != 1 {
		t.Errorf("Count A: got %d want 1", n)
	}
	if n, _ := storeB.Count(ctx); n != 1 {
		t.Errorf("Count B: got %d want 1", n)
	}

	// Reset is also user-scoped — clearing A must leave B intact.
	n, err := storeA.Reset(ctx)
	if err != nil {
		t.Fatalf("Reset A: %v", err)
	}
	if n != 1 {
		t.Errorf("Reset A returned %d, want 1", n)
	}
	gotB2, _ := storeB.ListRecent(ctx, 100)
	if len(gotB2) != 1 || gotB2[0].ID != "b-1" {
		t.Errorf("Reset A leaked into B: %+v", gotB2)
	}
}

// TestListRecent_FiltersShortDrives encodes the "≥1 min" filter in
// ListRecent. A row shorter than a minute is a spurious live segment
// (parking nudge, GPS jitter) and the UI shouldn't show it.
func TestListRecent_FiltersShortDrives(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	short := sampleDrive("short", "rv-A", start)
	short.EndedAt = start.Add(30 * time.Second)
	long := sampleDrive("long", "rv-A", start.Add(time.Hour))

	if err := store.Upsert(ctx, short); err != nil {
		t.Fatalf("Upsert short: %v", err)
	}
	if err := store.Upsert(ctx, long); err != nil {
		t.Fatalf("Upsert long: %v", err)
	}
	got, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 1 || got[0].ID != "long" {
		t.Errorf("filter ≥1min broken: %+v", got)
	}
	// Count is unfiltered — both rows present.
	if n, _ := store.Count(ctx); n != 2 {
		t.Errorf("Count: got %d want 2", n)
	}
}

// TestLiveOverlap: see charges.TestLiveOverlap.
func TestLiveOverlap(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	ghost := sampleDrive("ghost", "rv-A", start) // [start, start+30m], 80→70%, odo 10000→10025
	if err := store.Upsert(ctx, ghost); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	ov, err := store.LiveOverlap(ctx, "rv-A", "mine", start.Add(time.Second), start.Add(time.Hour))
	if err != nil || ov == nil {
		t.Fatalf("LiveOverlap = %v, %v; want the ghost row", ov, err)
	}
	if !ov.EndedAt.Equal(ghost.EndedAt) || ov.EndSoCPct != 70 || ov.EndOdometerMi != 10025 {
		t.Errorf("overlap = %+v", ov)
	}
	if ov, _ := store.LiveOverlap(ctx, "rv-A", "ghost", start, start.Add(time.Hour)); ov != nil {
		t.Errorf("own row must be excluded, got %+v", ov)
	}
}

// TestLiveCovers: see charges.TestLiveCovers.
func TestLiveCovers(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	store := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	if err := store.Upsert(ctx, sampleDrive("live-1", "rv-A", start)); err != nil { // 30 min, live
		t.Fatal(err)
	}
	if got, err := store.LiveCovers(ctx, "rv-A", start.Add(5*time.Minute), start.Add(time.Hour)); err != nil || !got {
		t.Errorf("overlapping range: %v, %v; want true", got, err)
	}
	if got, err := store.LiveCovers(ctx, "rv-A", start.Add(30*time.Minute), start.Add(time.Hour)); err != nil || got {
		t.Errorf("range starting at the live drive's end: %v, %v; want false", got, err)
	}
}
