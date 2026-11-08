//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/closestale"
	"github.com/apohor/rivolt/internal/repair/phantom"
	"github.com/apohor/rivolt/internal/repair/stretched"
)

// The stale-open repair against Postgres (#1641): it closes exactly the open-state rows of dormant accounts by the SoC rule, leaves a
// reporting vehicle's row, a user-touched row and a recent row alone, refuses a wrong count or digest, a failing backup and a charge in
// progress, changes nothing but final_state and the repair's metadata key, and the undo is all or nothing and returns the rows exactly.
func TestCloseStaleOpenRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mkVehicle := func(name string) (uuid.UUID, uuid.UUID) {
		uid, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		var vid uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,$2,135) RETURNING id`, uid, "rv-"+name).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		return uid, vid
	}
	old := time.Now().UTC().Add(-20 * 24 * time.Hour).Truncate(time.Hour) // vehicle_state is partitioned by month: stay inside the test database's partitions
	k := 0
	insert := func(uid, vid uuid.UUID, ext, state string, s0, s1 float64, set string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		start := old.Add(time.Duration(k) * 12 * time.Hour) // a vehicle's live charges must not overlap
		k++
		if err := pool.QueryRowContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, start_soc_pct, end_soc_pct, final_state, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'live',20,$6,$7,$8,$4,$5) RETURNING id`, uid, vid, ext, start, start.Add(10*time.Hour), s0, s1, state).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", ext, err)
		}
		if set != "" { // the touch trigger would stamp updated_at with now
			exec(`ALTER TABLE charges DISABLE TRIGGER charges_touch_updated_at`)
			exec(`UPDATE charges SET `+set+`, updated_at = $2 WHERE id = $1`, id, start.Add(10*time.Hour))
			exec(`ALTER TABLE charges ENABLE TRIGGER charges_touch_updated_at`)
		}
		return id
	}
	// A dormant account (no frame for months): an open row where the SoC rose, one where it did not.
	uD, vD := mkVehicle("dormant")
	exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source) VALUES ($1,$2,$3,'t')`, vD, old.Add(11*time.Hour), uD)
	rose := insert(uD, vD, "d1", "charging_ready", 28, 78, "")
	flat := insert(uD, vD, "d2", "charging_active", 50, 40, "")
	// Left alone: a user-priced row, a note, and a closed row.
	userPrice := insert(uD, vD, "d3", "charging_ready", 20, 60, "cost_source = 'user'")
	noted := insert(uD, vD, "d4", "charging_ready", 20, 60, "notes = 'mine'")
	closed := insert(uD, vD, "d5", "charging_complete", 20, 60, "")
	// A reporting vehicle: its open row is the janitor's.
	uR, vR := mkVehicle("reporting")
	exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source) VALUES ($1, now() - interval '1 hour', $2, 't')`, vR, uR)
	reporting := insert(uR, vR, "r1", "charging_ready", 20, 60, "")

	snap := func() map[uuid.UUID]string {
		m := map[uuid.UUID]string{}
		rs, err := pool.QueryContext(ctx, `SELECT id, (to_jsonb(c.*) - 'updated_at')::text FROM charges c`)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		for rs.Next() {
			var id uuid.UUID
			var j string
			if err := rs.Scan(&id, &j); err != nil {
				t.Fatal(err)
			}
			m[id] = j
		}
		return m
	}
	plan := func() closestale.Plan {
		rows, err := closestale.Select(ctx, pool, false)
		if err != nil {
			t.Fatal(err)
		}
		return closestale.MakePlan(rows, time.Now())
	}
	p := plan()
	got := map[uuid.UUID]string{}
	for _, id := range p.IDs() {
		got[id] = p.NewState[id]
	}
	if len(got) != 2 || got[rose] != "charging_complete" || got[flat] != "abandoned" {
		t.Fatalf("plan = %v, want the rose row complete and the flat row abandoned (skipped: %v; kept: %s %s %s %s)", got, p.Skipped, userPrice, noted, closed, reporting)
	}
	digest := p.Digest()
	before := snap()

	// A wrong count or digest, a failing backup sink, a charge in progress: nothing changes.
	if _, err := closestale.Apply(ctx, pool, uuid.New(), 3, digest, "t", false, nil); err == nil || !strings.Contains(err.Error(), "differ from the approved dry run") {
		t.Fatalf("wrong count: %v", err)
	}
	if _, err := closestale.Apply(ctx, pool, uuid.New(), 2, strings.Repeat("0", 64), "t", false, nil); err == nil {
		t.Fatal("a wrong digest must abort")
	}
	if _, err := closestale.Apply(ctx, pool, uuid.New(), 2, digest, "t", false, func([]phantom.BackupRow) error { return context.Canceled }); err == nil {
		t.Fatal("a failing backup sink must abort")
	}
	for id, j := range snap() {
		if before[id] != j {
			t.Fatalf("a refused apply changed row %s", id)
		}
	}

	batch := uuid.New()
	var sunk []phantom.BackupRow
	n, err := closestale.Apply(ctx, pool, batch, 2, digest, "t", false, func(r []phantom.BackupRow) error { sunk = r; return nil })
	if err != nil || n != 2 || len(sunk) != 2 {
		t.Fatalf("apply: %d %v (sunk %d)", n, err, len(sunk))
	}
	state := func(id uuid.UUID) (string, time.Time, string) {
		var st, meta string
		var ended time.Time
		if err := pool.QueryRowContext(ctx, `SELECT final_state, ended_at, metadata::text FROM charges WHERE id = $1`, id).Scan(&st, &ended, &meta); err != nil {
			t.Fatal(err)
		}
		return st, ended, meta
	}
	if st, ended, meta := state(rose); st != "charging_complete" || !ended.Equal(old.Add(10*time.Hour)) || !strings.Contains(meta, closestale.MarkerKey) {
		t.Fatalf("closed row: state=%s ended=%v meta=%s; want charging_complete, ended_at untouched, marked", st, ended, meta)
	}
	if st, _, _ := state(flat); st != "abandoned" {
		t.Fatalf("flat row = %s", st)
	}
	after := snap()
	for id, j := range after {
		if id != rose && id != flat && before[id] != j {
			t.Fatalf("apply changed a row it must not touch: %s", id)
		}
	}
	if q := plan(); len(q.IDs()) != 0 {
		t.Fatalf("a second pass would close %d more", len(q.IDs()))
	}

	// The undo is all or nothing: each of these blocks it, changes nothing, and is listed by the restore dry run.
	afterApply := snap()
	blocked := func(what, set, unset string) {
		t.Helper()
		exec(set)
		bl, err := closestale.RestoreBlockers(ctx, pool, batch)
		if err != nil || len(bl) == 0 {
			t.Fatalf("%s: restore dry run lists %v %v, want a blocker", what, bl, err)
		}
		if r, err := closestale.Restore(ctx, pool, batch); !errors.Is(err, closestale.ErrRestoreBlocked) || r != 0 {
			t.Fatalf("%s: restore = %d %v, want ErrRestoreBlocked", what, r, err)
		}
		exec(unset)
		if got := snap(); fmt.Sprint(got) != fmt.Sprint(afterApply) {
			t.Fatalf("%s: a blocked restore changed rows", what)
		}
	}
	blocked("a user prices a closed row", fmt.Sprintf(`UPDATE charges SET cost_source = 'user' WHERE id = '%s'`, rose), fmt.Sprintf(`UPDATE charges SET cost_source = NULL WHERE id = '%s'`, rose))
	blocked("a user notes a closed row", fmt.Sprintf(`UPDATE charges SET notes = 'mine' WHERE id = '%s'`, flat), fmt.Sprintf(`UPDATE charges SET notes = NULL WHERE id = '%s'`, flat))
	blocked("a closed row was reopened", fmt.Sprintf(`UPDATE charges SET final_state = 'charging_active' WHERE id = '%s'`, rose), fmt.Sprintf(`UPDATE charges SET final_state = 'charging_complete' WHERE id = '%s'`, rose))

	// Each tool restores only its own batches: the other repair's restore is refused, with nothing changed.
	if r, err := stretched.Restore(ctx, pool, batch); !errors.Is(err, stretched.ErrRestoreBlocked) || r != 0 {
		t.Fatalf("stretched.Restore of a close-stale batch = %d %v, want ErrRestoreBlocked", r, err)
	}
	if bl, err := stretched.RestoreBlockers(ctx, pool, batch); err != nil || len(bl) == 0 {
		t.Fatalf("stretched restore dry run lists %v %v, want blockers", bl, err)
	}
	if got := snap(); fmt.Sprint(got) != fmt.Sprint(afterApply) {
		t.Fatal("the other repair's refused restore changed rows")
	}

	// A key added to the metadata after the apply survives; the repair's own key goes; only final_state is put back.
	exec(`UPDATE charges SET metadata = metadata || '{"later": 1}'::jsonb WHERE id = $1`, rose)
	if r, err := closestale.Restore(ctx, pool, batch); err != nil || r != 2 {
		t.Fatalf("restore: %d %v", r, err)
	}
	if st, _, meta := state(rose); st != "charging_ready" || strings.Contains(meta, closestale.MarkerKey) || !strings.Contains(meta, `"later"`) {
		t.Fatalf("restored row: state=%s meta=%s", st, meta)
	}
	exec(`UPDATE charges SET metadata = metadata - 'later' WHERE id = $1`, rose)
	for id, j := range snap() {
		if before[id] != j {
			t.Fatalf("after restore row %s differs:\n before %s\n after  %s", id, before[id], j)
		}
	}
	if _, err := closestale.Restore(ctx, pool, batch); err == nil {
		t.Fatal("a second restore of the same batch must be refused")
	}

	// A charge in progress: the apply refuses, without --allow-open.
	exec(`UPDATE charges SET ended_at = now(), updated_at = now() WHERE id = $1`, reporting)
	if _, err := closestale.Apply(ctx, pool, uuid.New(), 2, digest, "t", false, nil); err == nil || !strings.Contains(err.Error(), "quiet hour") {
		t.Fatalf("open charge: %v, want refusal", err)
	}
}
