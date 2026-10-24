//go:build integration

package chargeenergy

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// Against real Postgres: the dry-run plan, one user's apply (and nobody else's), the guard against a row edited since, and
// the restore.
func TestPlanApplyRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	ua, _ := db.EnsureUser(ctx, pool, "ce-a")
	ub, _ := db.EnsureUser(ctx, pool, "ce-b")
	mkVeh := func(uid uuid.UUID, rid string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, pack_kwh) VALUES ($1,$2,'R1T',100) RETURNING id`, uid, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	va, vb := mkVeh(ua, "ce-veh-a"), mkVeh(ub, "ce-veh-b")
	since := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	ins := func(uid, vid uuid.UUID, ext string, start time.Time, energy, maxKW float64, costSource string, cost, price *float64) uuid.UUID {
		var id uuid.UUID
		err := pool.QueryRowContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, start_soc_pct, end_soc_pct, energy_added_kwh,
			                     max_power_kw, source, final_state, cost, price_per_kwh, currency, cost_source)
			VALUES ($1,$2,$3,$4,$5,20,50,$6,$7,'live','charging_complete',$8,$9,'USD',$10) RETURNING id`,
			uid, vid, ext, start, start.Add(8*time.Hour), energy, maxKW, cost, price, costSource).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f := func(v float64) *float64 { return &v }
	// Vehicle A: three clean charges before the cutoff (1.4 kWh per %), then a reset one, a fast one and a user-priced one.
	for i := 0; i < 3; i++ {
		ins(ua, va, "clean-"+string(rune('a'+i)), since.Add(-time.Duration(10+i)*24*time.Hour), 42, 7.5, "home_rate", f(8.4), f(0.2))
	}
	bad := ins(ua, va, "bad", since.Add(24*time.Hour), 5, 7.5, "home_rate", f(1), f(0.2))
	ins(ua, va, "fast", since.Add(48*time.Hour), 5, 150, "home_rate", f(1), f(0.2))
	ins(ua, va, "user", since.Add(72*time.Hour), 5, 7.5, "user", f(2), f(0.4))
	// Priced some other way, or with an unknown start SoC: left alone however low the energy.
	ins(ua, va, "net", since.Add(96*time.Hour), 5, 7.5, "network_estimate", f(1), f(0.2))
	nostart := ins(ua, va, "nostart", since.Add(120*time.Hour), 5, 7.5, "home_rate", f(1), f(0.2))
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET start_soc_pct = NULL WHERE id = $1`, nostart); err != nil {
		t.Fatal(err)
	}
	// Vehicle B (another user): no clean history, one reset charge.
	other := ins(ub, vb, "bad-b", since.Add(24*time.Hour), 5, 7.5, "home_rate", f(1), f(0.2))

	rows, clean, err := Load(ctx, pool, since)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan(rows, clean)
	var repair []uuid.UUID
	for _, p := range plan {
		if p.Verdict.Repair {
			repair = append(repair, p.Row.ID)
			if p.Row.ID == bad && (p.Est.Method != "own median of 3 clean charges" || p.Verdict.NewEnergy != 42) {
				t.Errorf("the bad row on A: %+v, want 42 kWh from the vehicle's own median", p)
			}
			if p.Row.ID == other && p.Est.Method != "pack x loss factor" {
				t.Errorf("B has no clean history, want the pack-based estimate: %+v", p.Est)
			}
		}
	}
	if len(repair) != 2 {
		t.Fatalf("%d rows to repair, want 2 (A's reset one and B's): %v", len(repair), repair)
	}

	// Apply for A only: B's row is not touched even though its id is on the list.
	backup := filepath.Join(t.TempDir(), "b.jsonl")
	changed, skipped, err := Apply(ctx, pool, ua, repair, since, backup)
	if err != nil || changed != 1 || skipped != 1 {
		t.Fatalf("apply for A: changed %d skipped %d err %v, want 1, 1", changed, skipped, err)
	}
	get := func(id uuid.UUID) (e float64, c sql.NullFloat64, md string) {
		if err := pool.QueryRowContext(ctx, `SELECT energy_added_kwh, cost, metadata::text FROM charges WHERE id=$1`, id).Scan(&e, &c, &md); err != nil {
			t.Fatal(err)
		}
		return
	}
	if e, c, md := get(bad); e != 42 || !c.Valid || c.Float64 != 8.4 || md == "{}" {
		t.Errorf("A's row: %.2f kWh, cost %v, metadata %s; want 42, 8.40 and a note", e, c, md)
	}
	if e, _, md := get(other); e != 5 || md != "{}" {
		t.Errorf("B's row changed: %.2f kWh, %s", e, md)
	}
	// A second apply finds nothing left to do (already estimated), and the backup file is never overwritten.
	if c, _, err := Apply(ctx, pool, ua, repair, since, filepath.Join(t.TempDir(), "b2.jsonl")); err != nil || c != 0 {
		t.Errorf("a second apply changed %d (err %v)", c, err)
	}

	// Restore puts it back; a row edited since is left alone.
	rb, err := ReadBackup(backup)
	if err != nil || len(rb) != 1 {
		t.Fatalf("backup: %v %v", rb, err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 41 WHERE id = $1`, bad); err != nil {
		t.Fatal(err)
	}
	if n, err := Restore(ctx, pool, rb); err != nil || n != 0 {
		t.Errorf("a row edited since was restored: %d %v", n, err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 42 WHERE id = $1`, bad); err != nil {
		t.Fatal(err)
	}
	if n, err := Restore(ctx, pool, rb); err != nil || n != 1 {
		t.Fatalf("restore: %d %v", n, err)
	}
	if e, c, md := get(bad); e != 5 || !c.Valid || c.Float64 != 1 || md != "{}" {
		t.Errorf("after restore: %.2f kWh, cost %v, metadata %s; want 5, 1.00, {}", e, c, md)
	}
}
