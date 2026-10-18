//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/phantom"
	"github.com/apohor/rivolt/internal/testpg"
)

// TestPhantomRepair: the rule keeps a real charge and a user-priced
// one, deletes a stale-copy one with its pack-health sample, the backup
// table holds the rows, and restore puts everything back exactly once.
func TestPhantomRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "phantom-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "phantom-b")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-ph',135) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	ins := func(ext string, off time.Duration, kwh, s0, s1 float64, costSource string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		st := base.Add(off)
		var cs any
		if costSource != "" {
			cs = costSource
		}
		if err := pool.QueryRowContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source,
			                     energy_added_kwh, start_soc_pct, end_soc_pct, final_state, cost_source)
			VALUES ($1,$2,$3,$4,$5,'live',$6,$7,$8,'abandoned',$9) RETURNING id`,
			uid, vid, ext, st, st.Add(20*time.Minute), kwh, s0, s1, cs).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", ext, err)
		}
		return id
	}
	phantomID := ins("p1", 0, 36.8, 80, 80, "")
	realID := ins("r1", 2*time.Hour, 30, 40, 62, "")
	userID := ins("u1", 4*time.Hour, 36.8, 80, 80, "user")
	small := ins("s1", 6*time.Hour, 3, 80, 80, "")
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_pack_health_samples (vehicle_id, charge_id, at, pack_kwh_effective, soc_delta_pct, energy_delivered_kwh) VALUES ($1,$2,$3,135,1,36.8)`, vid, phantomID, base); err != nil {
		t.Fatal(err)
	}
	// Active power under the phantom would save it; there is none.
	all := []uuid.UUID{phantomID, realID, userID, small}

	// Another user's view of the same ids loads nothing.
	if rows, err := phantom.Load(ctx, pool, other, all); err != nil || len(rows) != 0 {
		t.Fatalf("cross-user load: %v %v", rows, err)
	}
	rows, err := phantom.Load(ctx, pool, uid, all)
	if err != nil || len(rows) != 4 {
		t.Fatalf("load: %d %v", len(rows), err)
	}
	var del []uuid.UUID
	for _, r := range rows {
		if phantom.Classify(r).Delete {
			del = append(del, r.ID)
		}
	}
	if len(del) != 1 || del[0] != phantomID {
		t.Fatalf("classified %v, want only %s", del, phantomID)
	}

	// Applying a non-phantom is refused and deletes nothing.
	batch := uuid.New()
	if _, err := phantom.Apply(ctx, pool, batch, uid, vid, []uuid.UUID{phantomID, realID}, "t", nil); err == nil {
		t.Fatal("real charge in the list must abort the vehicle")
	}
	var n int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id=$1`, uid).Scan(&n)
	if n != 4 {
		t.Fatalf("rows after refused apply: %d", n)
	}

	got, err := phantom.Apply(ctx, pool, batch, uid, vid, []uuid.UUID{phantomID}, "t", nil)
	if err != nil || got != 1 {
		t.Fatalf("apply: %d %v", got, err)
	}
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id=$1`, uid).Scan(&n)
	if n != 3 {
		t.Fatalf("rows after apply: %d", n)
	}
	var bc, bp int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE kind='charge'), count(*) FILTER (WHERE kind='pack_health_sample') FROM charges_deleted_backup WHERE batch_id=$1`, batch).Scan(&bc, &bp)
	if bc != 1 || bp != 1 {
		t.Fatalf("backup rows charge=%d pack=%d", bc, bp)
	}

	res, err := phantom.Restore(ctx, pool, batch)
	if err != nil || res.Charges != 1 || res.PackHealth != 1 {
		t.Fatalf("restore: %+v %v", res, err)
	}
	var ext string
	var kwh float64
	if err := pool.QueryRowContext(ctx, `SELECT external_id, energy_added_kwh FROM charges WHERE id=$1`, phantomID).Scan(&ext, &kwh); err != nil || ext != "p1" || kwh != 36.8 {
		t.Fatalf("restored row: %q %v %v", ext, kwh, err)
	}
	if _, err := phantom.Restore(ctx, pool, batch); err == nil {
		t.Fatal("second restore must be refused")
	}
}

// TestOpenNoMigrate: repair commands connect without applying any
// migration, and RequireTables names what a stale schema lacks.
func TestOpenNoMigrate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := testpg.Fresh(ctx, t) // a database with no migrations applied
	d, err := db.OpenNoMigrate(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.RequireTables(ctx, d, "charges"); err == nil {
		t.Fatal("fresh database must be refused, not migrated")
	}
	if latest, _ := db.LatestApplied(ctx, d); latest != "" {
		t.Fatalf("OpenNoMigrate applied %q", latest)
	}
	m, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err := db.RequireTables(ctx, d, "charges", "charge_restamps"); err != nil {
		t.Fatal(err)
	}
}
