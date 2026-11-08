//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/phantom"
	"github.com/apohor/rivolt/internal/repair/stretched"
)

// The stretched-row repair against Postgres: it caps exactly the closed, untouched rows whose ended_at ran past the
// last plugged frame through a drive, flags (does not change) a row with plugged frames after a drive, leaves the
// rest, refuses a count or digest that is not the approved dry run's, applies in one transaction with the old rows
// backed up, never moves ended_at later, and the restore brings every row back identical in every column but updated_at, which the table's touch trigger owns (#1630).
func TestStretchedRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "stretch-a")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-stretch',135) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// A charge row: started at base+off, ended 20 h later, closed, written 30 h ago.
	row := func(ext string, off time.Duration, state, set string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		st := base.Add(off)
		if err := pool.QueryRowContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, start_soc_pct, end_soc_pct, final_state, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'live',16.8,47,76,$6,$4,$7) RETURNING id`,
			uid, vid, ext, st, st.Add(20*time.Hour), state, time.Now().Add(-30*time.Hour)).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", ext, err)
		}
		if set != "" {
			// The touch trigger would stamp updated_at with now; the row must look written 30 h ago.
			exec(`ALTER TABLE charges DISABLE TRIGGER charges_touch_updated_at`)
			exec(`UPDATE charges SET `+set+` WHERE id = $1`, id)
			exec(`ALTER TABLE charges ENABLE TRIGGER charges_touch_updated_at`)
		}
		return id
	}
	// A plugged frame (status connected) and a drive at offsets from a row's start.
	frame := func(at time.Time, status string) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source, charger_status, charging_state) VALUES ($1,$2,$3,'t',$4,'charging_ready')`, vid, at, uid, status)
	}
	drive := func(ext string, at time.Time) {
		exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,$3,$4,$5,5,'live')`, uid, vid, ext, at, at.Add(20*time.Minute))
	}
	// Each row is spaced 24 h apart so frames and drives are its own (charges_live_no_overlap).
	stretch := row("s1", 0, "charging_user_stopped", "") // plugged for 2 h, drive at 3 h, unplugged after
	frame(base.Add(1*time.Hour), "chrgr_sts_connected_charging")
	frame(base.Add(2*time.Hour), "chrgr_sts_connected_charging")
	frame(base.Add(4*time.Hour), "chrgr_sts_not_connected")
	drive("d-s1", base.Add(3*time.Hour))
	// Wait: the same vehicle's rows must not overlap, so the other rows live on later days via the vehicle's own offsets.
	b2 := base.Add(24 * time.Hour)
	flagged := row("f1", 24*time.Hour, "charging_complete", "") // plugged frames after the drive: flagged
	frame(b2.Add(1*time.Hour), "chrgr_sts_connected_charging")
	drive("d-f1", b2.Add(2*time.Hour))
	frame(b2.Add(5*time.Hour), "chrgr_sts_connected_charging")
	b3 := base.Add(48 * time.Hour)
	userPrice := row("u1", 48*time.Hour, "charging_user_stopped", "cost_source='user'")
	frame(b3.Add(1*time.Hour), "chrgr_sts_connected_charging")
	drive("d-u1", b3.Add(3*time.Hour))
	_ = userPrice

	digestOf := func() (stretched.Plan, string) {
		rows, err := stretched.Select(ctx, pool, false)
		if err != nil {
			t.Fatal(err)
		}
		p := stretched.MakePlan(rows, time.Now(), nil)
		return p, p.Digest()
	}
	plan, digest := digestOf()
	if ids := plan.IDs(); len(ids) != 1 || ids[0] != stretch {
		t.Fatalf("plan caps %v, want exactly the stretched row %s", ids, stretch)
	}
	if f := plan.FlaggedIDs(); len(f) != 1 || f[0] != flagged {
		t.Fatalf("flagged %v, want %s", f, flagged)
	}
	wantEnd := base.Add(2 * time.Hour)
	if got := plan.CapTo[stretch]; !got.Equal(wantEnd) {
		t.Fatalf("cap = %v, want the last plugged frame %v", got, wantEnd)
	}

	snapshot := func() map[uuid.UUID]string {
		m := map[uuid.UUID]string{}
		rs, err := pool.QueryContext(ctx, `SELECT id, (to_jsonb(c.*) - 'updated_at')::text FROM charges c WHERE user_id = $1`, uid)
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
	before := snapshot()

	// A wrong count or digest: nothing changed.
	if _, err := stretched.Apply(ctx, pool, uuid.New(), 2, digest, "t", false, nil, nil); err == nil || !strings.Contains(err.Error(), "differ from the approved dry run") {
		t.Fatalf("wrong count: %v", err)
	}
	if _, err := stretched.Apply(ctx, pool, uuid.New(), 1, strings.Repeat("0", 64), "t", false, nil, nil); err == nil {
		t.Fatal("a wrong digest must abort")
	}
	for id, j := range snapshot() {
		if before[id] != j {
			t.Fatalf("a refused apply changed row %s", id)
		}
	}
	// A failing backup sink aborts before anything changes.
	if _, err := stretched.Apply(ctx, pool, uuid.New(), 1, digest, "t", false, nil, func([]phantom.BackupRow) error { return context.Canceled }); err == nil {
		t.Fatal("a failing backup sink must abort")
	}
	for id, j := range snapshot() {
		if before[id] != j {
			t.Fatalf("an aborted apply changed row %s", id)
		}
	}

	batch := uuid.New()
	var sunk []phantom.BackupRow
	n, err := stretched.Apply(ctx, pool, batch, 1, digest, "t", false, nil, func(r []phantom.BackupRow) error { sunk = r; return nil })
	if err != nil || n != 1 || len(sunk) != 1 {
		t.Fatalf("apply: %d %v (sunk %d)", n, err, len(sunk))
	}
	after := snapshot()
	var gotEnd time.Time
	_ = pool.QueryRowContext(ctx, `SELECT ended_at FROM charges WHERE id = $1`, stretch).Scan(&gotEnd)
	if !gotEnd.Equal(wantEnd) {
		t.Fatalf("ended_at after apply = %v, want %v", gotEnd, wantEnd)
	}
	for id, j := range after {
		if id != stretch && before[id] != j {
			t.Fatalf("apply changed a row it must not touch: %s", id)
		}
	}
	// A second pass finds nothing more to cap.
	if p, _ := digestOf(); len(p.Cap) != 0 {
		t.Fatalf("second pass would cap %d more", len(p.Cap))
	}

	// Undo: the row comes back byte-identical, the batch is marked restored and cannot be restored twice.
	restored, skipped, err := stretched.Restore(ctx, pool, batch)
	if err != nil || restored != 1 || skipped != 0 {
		t.Fatalf("restore: %d restored %d skipped %v", restored, skipped, err)
	}
	for id, j := range snapshot() {
		if before[id] != j {
			t.Fatalf("after restore row %s differs:\n before %s\n after  %s", id, before[id], j)
		}
	}
	if _, _, err := stretched.Restore(ctx, pool, batch); err == nil {
		t.Fatal("a second restore of the same batch must be refused")
	}
	// The restore touched updated_at, so the row is quiet for an hour before a dry run would pick it up again.
	if p, _ := digestOf(); len(p.Cap) != 0 || p.Skipped["written in the last hour"] != 1 || p.Skipped["carries a user-entered price"] != 1 {
		t.Fatalf("after restore: cap %d, skipped %v; want the row held back as just written and the user price refused", len(p.Cap), p.Skipped)
	}
}
