//go:build integration

package routegaps_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/repair/routegaps"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

type shapeFiller struct{ calls int }

func (f *shapeFiller) RouteShape(_ context.Context, from, to [2]float64) ([][2]float64, error) {
	f.calls++
	mid := [2]float64{(from[0] + to[0]) / 2, from[1] + 0.0005}
	return [][2]float64{from, {from[0] + (mid[0]-from[0])/2, mid[1]}, mid, {mid[0] + (to[0]-mid[0])/2, mid[1]}, to}, nil
}

// Select only closed live drives with a route in the window; Apply rewrites only the planned drives, writes the backup row before the
// update, and leaves a drive whose route changed since the plan; Restore puts the old route back only where the filled one is still
// stored.
func TestRouteGapsSelectApplyRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, _ := db.EnsureUser(ctx, pool, "routegaps")
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	gappy := rivian.EncodePolyline([][2]float64{{30.000, -100}, {30.001, -100}, {30.012, -100}, {30.013, -100}}) // one ~1.2 km dropout
	clean := rivian.EncodePolyline([][2]float64{{30.000, -100}, {30.001, -100}, {30.002, -100}})
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	put := func(id, source string, at time.Time, route string, ended time.Time) {
		t.Helper()
		d := drives.Drive{ID: id, VehicleID: "rv-routegaps", StartedAt: at, EndedAt: ended, StartSoCPct: 80, EndSoCPct: 70,
			DistanceMi: 5, EnergyUsedKWh: 2, Source: source, RoutePolyline: route}
		if err := store.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	put("rg-gappy", "live", start, gappy, start.Add(30*time.Minute))
	put("rg-gappy2", "live", start.Add(2*time.Hour), gappy, start.Add(2*time.Hour+30*time.Minute))
	put("rg-clean", "live", start.Add(4*time.Hour), clean, start.Add(4*time.Hour+30*time.Minute))
	put("rg-imported", "electrafi", start.Add(6*time.Hour), gappy, start.Add(6*time.Hour+30*time.Minute))
	put("rg-noroute", "live", start.Add(8*time.Hour), "", start.Add(8*time.Hour+30*time.Minute))
	put("rg-recent", "live", time.Now().UTC().Add(-20*time.Minute), gappy, time.Now().UTC().Add(-5*time.Minute))
	put("rg-old", "live", start.Add(-90*24*time.Hour), gappy, start.Add(-90*24*time.Hour+30*time.Minute))

	cands, err := routegaps.Select(ctx, pool, start.Add(-time.Hour), time.Now().UTC().Add(time.Hour))
	if err != nil || len(cands) != 3 {
		t.Fatalf("selected %d (%v), want the 3 closed live drives with a route in the window", len(cands), err)
	}
	plan := routegaps.MakePlan(cands, routegaps.DefaultMinGapM)
	if len(plan.Drives) != 2 || plan.GapsTotal != 2 {
		t.Fatalf("plan: %d drives, %d dropouts; want the 2 gappy drives", len(plan.Drives), plan.GapsTotal)
	}

	// One planned drive changes between the plan and the apply: it must be left alone.
	var raced string
	for _, c := range plan.Drives {
		if c.StartedAt.Equal(start.Add(2 * time.Hour)) {
			raced = c.ID.String()
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE drives SET route_polyline = $2 WHERE id = $1`, raced, clean); err != nil {
		t.Fatal(err)
	}

	var backups []routegaps.BackupRow
	f := &shapeFiller{}
	res, err := routegaps.Apply(ctx, pool, plan, f, func(r routegaps.BackupRow) error { backups = append(backups, r); return nil }, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Raced != 1 || res.Filled != 2 || len(backups) != 2 {
		t.Fatalf("%+v, %d backup rows; want 1 updated, 1 raced, a backup row for each attempted update", res, len(backups))
	}
	get := func(id string) string {
		var r string
		if err := pool.QueryRowContext(ctx, `SELECT route_polyline FROM drives WHERE external_id = $1`, id).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	filled, _ := routegaps.Decode(get("rg-gappy"))
	if len(filled) != 4+3 || len(routegaps.Gaps(filled, routegaps.DefaultMinGapM)) != 0 {
		t.Errorf("filled route has %d points and %d dropouts, want 7 and none", len(filled), len(routegaps.Gaps(filled, routegaps.DefaultMinGapM)))
	}
	if get("rg-gappy2") != clean || get("rg-clean") != clean || get("rg-imported") != gappy {
		t.Error("a drive that was not planned, or changed since, was rewritten")
	}

	// Restore: the filled drive goes back; the raced one (never filled) and a drive edited since are skipped.
	var applied []routegaps.BackupRow
	for _, b := range backups {
		if b.Old == gappy {
			applied = append(applied, b)
		}
	}
	rr, err := routegaps.Restore(ctx, pool, applied)
	if err != nil || rr.Restored != 1 {
		t.Fatalf("restore: %+v %v", rr, err)
	}
	if get("rg-gappy") != gappy {
		t.Error("the old route did not come back")
	}
	rr, err = routegaps.Restore(ctx, pool, applied)
	if err != nil || rr.Restored != 0 || rr.Skipped != len(applied) {
		t.Errorf("a second restore touched something: %+v %v", rr, err)
	}
}
