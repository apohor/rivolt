//go:build integration

package routepoints_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/repair/routegaps"
	"github.com/apohor/rivolt/internal/repair/routepoints"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// encodeRaw is a precision-5 encoder with no guard (rivian.EncodePolyline refuses a point off the globe now), to build a corrupt stored route.
func encodeRaw(pts [][2]float64) string {
	out := []byte{}
	var pl, pn int
	put := func(v int) {
		v <<= 1
		if v < 0 {
			v = ^v
		}
		for v >= 0x20 {
			out = append(out, byte((0x20|(v&0x1f))+63))
			v >>= 5
		}
		out = append(out, byte(v+63))
	}
	for _, p := range pts {
		la, lo := int(p[0]*1e5+0.5), int(p[1]*1e5+0.5)
		if p[0] < 0 {
			la = int(p[0]*1e5 - 0.5)
		}
		if p[1] < 0 {
			lo = int(p[1]*1e5 - 0.5)
		}
		put(la - pl)
		put(lo - pn)
		pl, pn = la, lo
	}
	return string(out)
}

// Select reads every closed drive with a route, whatever its source; Apply rewrites only the planned drives without their bad points,
// writes the backup row before the update and leaves a drive whose route changed since the audit; routegaps.Restore puts the old route
// back only where the repaired one is still stored. Plainly fake places near (10, 10).
func TestRoutePointsSelectApplyRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, _ := db.EnsureUser(ctx, pool, "routepoints")
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	good := [][2]float64{{10.000, 10.000}, {10.001, 10.001}, {10.002, 10.002}, {10.003, 10.003}}
	corrupt := encodeRaw([][2]float64{good[0], good[1], {100.25, -970.1}, good[2], good[3]})
	clean := rivian.EncodePolyline(good)
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	put := func(id, source string, at time.Time, route string, ended time.Time) {
		t.Helper()
		d := drives.Drive{ID: id, VehicleID: "rv-routepoints", StartedAt: at, EndedAt: ended, StartSoCPct: 80, EndSoCPct: 70,
			DistanceMi: 5, EnergyUsedKWh: 2, Source: source, RoutePolyline: route}
		if err := store.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	put("rp-bad", "live", start, corrupt, start.Add(30*time.Minute))
	put("rp-bad2", "live", start.Add(2*time.Hour), corrupt, start.Add(2*time.Hour+30*time.Minute))
	put("rp-bad-imported", "electrafi", start.Add(3*time.Hour), corrupt, start.Add(3*time.Hour+30*time.Minute)) // any source
	// The cause itself: a fill vertex ten times too large. It goes back where it belongs, not away.
	mid := [2]float64{10.0005, 10.0005}
	scaled := encodeRaw([][2]float64{good[0], {mid[0] * 10, mid[1] * 10}, good[1], good[2], good[3]})
	repairedScaled := rivian.EncodePolyline([][2]float64{good[0], mid, good[1], good[2], good[3]})
	put("rp-scaled", "live", start.Add(5*time.Hour), scaled, start.Add(5*time.Hour+30*time.Minute))
	put("rp-clean", "live", start.Add(4*time.Hour), clean, start.Add(4*time.Hour+30*time.Minute))
	put("rp-noroute", "live", start.Add(8*time.Hour), "", start.Add(8*time.Hour+30*time.Minute))
	put("rp-recent", "live", time.Now().UTC().Add(-20*time.Minute), corrupt, time.Now().UTC().Add(-5*time.Minute)) // may still be written to

	cands, err := routepoints.Select(ctx, pool, start.Add(-time.Hour), time.Now().UTC().Add(time.Hour))
	if err != nil || len(cands) != 5 {
		t.Fatalf("selected %d (%v), want the 5 closed drives with a route", len(cands), err)
	}
	plan := routepoints.MakePlan(cands)
	if len(plan.Drives) != 4 || plan.BadPoints != 4 || plan.Rescaled != 1 || plan.Dropped != 3 {
		t.Fatalf("plan: %d drives, %d bad points (%d rescaled, %d dropped); want 4, 4 (1, 3)", len(plan.Drives), plan.BadPoints, plan.Rescaled, plan.Dropped)
	}

	// One planned drive changes between the audit and the apply: it must be left alone.
	var raced string
	for _, c := range plan.Drives {
		if c.StartedAt.Equal(start.Add(2 * time.Hour)) {
			raced = c.ID.String()
		}
	}
	// It becomes a route that is neither the corrupt one nor the repaired one (a restore matches on the hash of the repaired route, so a route
	// that happened to equal it would be restored: that is the restore doing its job, not a race).
	edited := rivian.EncodePolyline(good[:3])
	if _, err := pool.ExecContext(ctx, `UPDATE drives SET route_polyline = $2 WHERE id = $1`, raced, edited); err != nil {
		t.Fatal(err)
	}

	var backups []routegaps.BackupRow
	res, err := routepoints.Apply(ctx, pool, plan, func(r routegaps.BackupRow) error { backups = append(backups, r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 3 || res.Raced != 1 || res.Rescaled != 1 || res.Dropped != 2 || len(backups) != 4 {
		t.Fatalf("%+v, %d backup rows; want 3 updated, 1 raced, 1 point put back, 2 dropped, a backup row for each attempted update", res, len(backups))
	}
	get := func(id string) string {
		var r string
		if err := pool.QueryRowContext(ctx, `SELECT route_polyline FROM drives WHERE external_id = $1`, id).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if get("rp-bad") != clean || get("rp-bad-imported") != clean {
		t.Error("a planned drive was not repaired to the route without its bad point")
	}
	if get("rp-scaled") != repairedScaled {
		t.Error("a tenfold vertex was not put back where it belongs")
	}
	if get("rp-bad2") != edited || get("rp-clean") != clean {
		t.Error("a drive that changed since the audit, or was never planned, was rewritten")
	}
	if get("rp-recent") != corrupt {
		t.Error("a drive that ended in the last hour was rewritten")
	}

	// Restore: the repaired drives go back; a second restore touches nothing.
	var applied []routegaps.BackupRow
	for _, b := range backups {
		if b.Old == corrupt || b.Old == scaled {
			applied = append(applied, b)
		}
	}
	rr, err := routegaps.Restore(ctx, pool, applied)
	if err != nil || rr.Restored != 3 || rr.Skipped != 1 {
		t.Fatalf("restore: %+v %v; want 3 restored and the drive edited since the repair skipped", rr, err)
	}
	if get("rp-bad2") != edited {
		t.Error("a drive edited after the repair was overwritten by the restore")
	}
	if get("rp-bad") != corrupt || get("rp-bad-imported") != corrupt || get("rp-scaled") != scaled {
		t.Error("the old routes did not come back")
	}
	rr, err = routegaps.Restore(ctx, pool, applied)
	if err != nil || rr.Restored != 0 {
		t.Errorf("a second restore touched something: %+v %v", rr, err)
	}
}
