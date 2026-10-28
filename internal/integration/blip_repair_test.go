//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/blips"
	"github.com/apohor/rivolt/internal/repair/phantom"
)

// The wake-blip repair against Postgres: it finds exactly the blips and nothing a person touched or
// still open, refuses to run on a count or digest that is not the approved dry run's (deleting
// nothing), refuses while a truck is charging, deletes in one transaction with the rows (and a
// cascading pack-health sample) backed up, and the backup restores them exactly.
func TestBlipRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "blip-a")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-blip',135) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	ins := func(ext string, off, dur time.Duration, s0, s1 float64, state string, set string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		st := base.Add(off)
		if err := pool.QueryRowContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, start_soc_pct, end_soc_pct, final_state, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'live',0.126,$6,$7,$8,$4,$4) RETURNING id`,
			uid, vid, ext, st, st.Add(dur), s0, s1, state).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", ext, err)
		}
		if set != "" {
			if _, err := pool.ExecContext(ctx, `UPDATE charges SET `+set+` WHERE id = $1`, id); err != nil {
				t.Fatalf("set %s: %v", ext, err)
			}
		}
		return id
	}
	b1 := ins("b1", 0, 1600*time.Millisecond, 80.1, 80.1, "charging_complete", "")
	b2 := ins("b2", time.Hour, 4*time.Second, 80, 80.2, "charging_complete", "")
	b3 := ins("b3", 2*time.Hour, 1700*time.Millisecond, 66.3, 66.3, "charging_scheduled", "")
	real1 := ins("r1", 3*time.Hour, 8*time.Second, 69.4, 70, "charging_complete", "") // moved 0.6
	long := ins("l1", 4*time.Hour, 30*time.Second, 65, 65, "charging_complete", "")   // not under 20 s
	open := ins("o1", 5*time.Hour, 5*time.Second, 50, 50, "charging_active", "")      // open
	userPrice := ins("u1", 6*time.Hour, 2*time.Second, 80, 80, "charging_complete", "cost_source='user'")
	noted := ins("n1", 7*time.Hour, 2*time.Second, 80, 80, "charging_complete", "notes='mine'")
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_pack_health_samples (vehicle_id, charge_id, at, pack_kwh_effective, soc_delta_pct, energy_delivered_kwh) VALUES ($1,$2,$3,135,0.1,0.126)`, vid, b1, base); err != nil {
		t.Fatal(err)
	}
	// An old open row (days ago) is not "a truck charging now"; the open one above is old too.
	rows, err := blips.Select(ctx, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	plan := blips.MakePlan(rows)
	got := map[uuid.UUID]bool{}
	for _, id := range plan.IDs() {
		got[id] = true
	}
	want := map[uuid.UUID]bool{b1: true, b2: true, b3: true}
	if len(got) != 3 || !got[b1] || !got[b2] || !got[b3] {
		t.Fatalf("plan = %v, want exactly the three blips %v (kept: real %s long %s open %s userPrice %s noted %s)", got, want, real1, long, open, userPrice, noted)
	}
	ids := plan.IDs()
	digest := blips.Digest(ids)
	if n, err := blips.OpenCharges(ctx, pool); err != nil || n != 0 {
		t.Fatalf("open charges now: %d %v, want 0 (the open row is days old)", n, err)
	}

	count := func() int {
		var n int
		_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id=$1`, uid).Scan(&n)
		return n
	}
	if count() != 8 {
		t.Fatalf("setup: %d rows", count())
	}

	// A wrong count, a wrong digest: nothing deleted.
	if _, err := blips.Apply(ctx, pool, uuid.New(), 2, digest, "t", false, nil); err == nil || !strings.Contains(err.Error(), "differ from the approved dry run") {
		t.Fatalf("wrong count: %v", err)
	}
	if _, err := blips.Apply(ctx, pool, uuid.New(), 3, strings.Repeat("0", 64), "t", false, nil); err == nil {
		t.Fatal("a wrong digest must abort")
	}
	if count() != 8 {
		t.Fatalf("a refused apply deleted rows: %d left", count())
	}

	// A truck charging now: refused.
	live := ins("live-now", 47*time.Hour, 2*time.Minute, 50, 52, "charging_active", "")
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET ended_at = now(), updated_at = now() WHERE id = $1`, live); err != nil {
		t.Fatal(err)
	}
	if _, err := blips.Apply(ctx, pool, uuid.New(), 3, digest, "t", false, nil); err == nil || !strings.Contains(err.Error(), "quiet hour") {
		t.Fatalf("open charge: %v, want refusal", err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM charges WHERE id = $1`, live); err != nil {
		t.Fatal(err)
	}

	// The approved run.
	batch := uuid.New()
	var sunk []phantom.BackupRow
	n, err := blips.Apply(ctx, pool, batch, 3, digest, "wake blip", false, func(r []phantom.BackupRow) error { sunk = r; return nil })
	if err != nil || n != 3 {
		t.Fatalf("apply: %d %v", n, err)
	}
	if count() != 5 {
		t.Fatalf("rows after apply: %d, want 5", count())
	}
	for _, kept := range []uuid.UUID{real1, long, open, userPrice, noted} {
		var c int
		_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE id=$1`, kept).Scan(&c)
		if c != 1 {
			t.Fatalf("a row that must stay was deleted: %s", kept)
		}
	}
	var bc, bp int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE kind='charge'), count(*) FILTER (WHERE kind='pack_health_sample') FROM charges_deleted_backup WHERE batch_id=$1`, batch).Scan(&bc, &bp)
	if bc != 3 || bp != 1 || len(sunk) != 4 {
		t.Fatalf("backup rows: charge=%d pack=%d, CSV sink got %d", bc, bp, len(sunk))
	}
	// A second pass finds nothing more.
	rows, _ = blips.Select(ctx, pool, false)
	if p := blips.MakePlan(rows); len(p.Delete) != 0 {
		t.Fatalf("second pass would delete %d more", len(p.Delete))
	}

	// Undo.
	res, err := phantom.Restore(ctx, pool, batch)
	if err != nil || res.Charges != 3 || res.PackHealth != 1 {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if count() != 8 {
		t.Fatalf("rows after restore: %d, want 8", count())
	}
	var ext string
	var kwh float64
	if err := pool.QueryRowContext(ctx, `SELECT external_id, energy_added_kwh FROM charges WHERE id=$1`, b1).Scan(&ext, &kwh); err != nil || ext != "b1" || kwh != 0.126 {
		t.Fatalf("restored row: %q %v %v", ext, kwh, err)
	}
	rows, _ = blips.Select(ctx, pool, false)
	if Digest2 := blips.Digest(blips.MakePlan(rows).IDs()); Digest2 != digest {
		t.Fatalf("after restore the dry run differs from before: %s vs %s", Digest2, digest)
	}
}
