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
	base := time.Now().UTC().Add(-120 * time.Hour).Truncate(time.Hour)
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
	frameS := func(at time.Time, status, state string, kw, mph float64) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source, charger_status, charging_state, charger_power_kw, speed_mph) VALUES ($1,$2,$3,'t',$4,$5,$6,$7)`, vid, at, uid, status, state, kw, mph)
	}
	frame := func(at time.Time, status string) { frameS(at, status, "charging_ready", 0, 0) }
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
	frameS(b2.Add(5*time.Hour), "chrgr_sts_connected_charging", "charging_active", 7, 0) // a real charge after the drive
	b3 := base.Add(48 * time.Hour)
	userPrice := row("u1", 48*time.Hour, "charging_user_stopped", "cost_source='user'")
	frame(b3.Add(1*time.Hour), "chrgr_sts_connected_charging")
	drive("d-u1", b3.Add(3*time.Hour))
	_ = userPrice
	// A status that sticks at "connected" through a drive, with nothing charging after it: capped, not flagged.
	b4 := base.Add(72 * time.Hour)
	sticky := row("k1", 72*time.Hour, "charging_user_stopped", "")
	frame(b4.Add(1*time.Hour), "chrgr_sts_connected_charging")
	frame(b4.Add(2*time.Hour), "chrgr_sts_connected_charging")
	drive("d-k1", b4.Add(3*time.Hour))
	frameS(b4.Add(3*time.Hour+5*time.Minute), "chrgr_sts_connected_no_chrg", "charging_ready", 0, 30) // moving, status stuck
	frameS(b4.Add(3*time.Hour+15*time.Minute), "chrgr_sts_connected_no_chrg", "charging_ready", 0, 30)
	frame(b4.Add(4*time.Hour), "chrgr_sts_connected_no_chrg") // parked, still "connected", no power
	frame(b4.Add(6*time.Hour), "chrgr_sts_connected_no_chrg")

	digestOf := func() (stretched.Plan, string) {
		rows, err := stretched.Select(ctx, pool, false, false)
		if err != nil {
			t.Fatal(err)
		}
		p := stretched.MakePlan(rows, time.Now(), stretched.Options{})
		return p, p.Digest()
	}
	plan, digest := digestOf()
	if ids := plan.IDs(); len(ids) != 2 || (ids[0] != stretch && ids[1] != stretch) || (ids[0] != sticky && ids[1] != sticky) {
		t.Fatalf("plan caps %v, want the stretched row %s and the sticky-status row %s", ids, stretch, sticky)
	}
	if got := plan.CapTo[sticky]; !got.Equal(b4.Add(2 * time.Hour)) {
		t.Fatalf("sticky row cap = %v, want its last standing plugged frame %v", got, b4.Add(2*time.Hour))
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
	if _, err := stretched.Apply(ctx, pool, uuid.New(), 3, digest, "t", false, stretched.Options{}, nil); err == nil || !strings.Contains(err.Error(), "differ from the approved dry run") {
		t.Fatalf("wrong count: %v", err)
	}
	if _, err := stretched.Apply(ctx, pool, uuid.New(), 2, strings.Repeat("0", 64), "t", false, stretched.Options{}, nil); err == nil {
		t.Fatal("a wrong digest must abort")
	}
	for id, j := range snapshot() {
		if before[id] != j {
			t.Fatalf("a refused apply changed row %s", id)
		}
	}
	// A failing backup sink aborts before anything changes.
	if _, err := stretched.Apply(ctx, pool, uuid.New(), 2, digest, "t", false, stretched.Options{}, func([]phantom.BackupRow) error { return context.Canceled }); err == nil {
		t.Fatal("a failing backup sink must abort")
	}
	for id, j := range snapshot() {
		if before[id] != j {
			t.Fatalf("an aborted apply changed row %s", id)
		}
	}

	batch := uuid.New()
	var sunk []phantom.BackupRow
	n, err := stretched.Apply(ctx, pool, batch, 2, digest, "t", false, stretched.Options{}, func(r []phantom.BackupRow) error { sunk = r; return nil })
	if err != nil || n != 2 || len(sunk) != 2 {
		t.Fatalf("apply: %d %v (sunk %d)", n, err, len(sunk))
	}
	after := snapshot()
	var gotEnd time.Time
	_ = pool.QueryRowContext(ctx, `SELECT ended_at FROM charges WHERE id = $1`, stretch).Scan(&gotEnd)
	if !gotEnd.Equal(wantEnd) {
		t.Fatalf("ended_at after apply = %v, want %v", gotEnd, wantEnd)
	}
	for id, j := range after {
		if id != stretch && id != sticky && before[id] != j {
			t.Fatalf("apply changed a row it must not touch: %s", id)
		}
	}
	// A second pass finds nothing more to cap.
	if p, _ := digestOf(); len(p.Cap) != 0 {
		t.Fatalf("second pass would cap %d more", len(p.Cap))
	}

	// Undo: the row comes back byte-identical, the batch is marked restored and cannot be restored twice.
	restored, skipped, err := stretched.Restore(ctx, pool, batch)
	if err != nil || restored != 2 || skipped != 0 {
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
	if p, _ := digestOf(); len(p.Cap) != 0 || p.Skipped["written in the last hour"] != 2 || p.Skipped["carries a user-entered price"] != 1 {
		t.Fatalf("after restore: cap %d, skipped %v; want the row held back as just written and the user price refused", len(p.Cap), p.Skipped)
	}
}

// The split against Postgres: a stretched row that swallowed a real charge after a drive becomes two rows. The original
// keeps the stretch before the drive (ended at its last plugged frame, with the SoC there), the parked segment after the
// drive becomes a new row with its own external id, energy is conserved, nothing is priced by copy, and the undo
// deletes the created row and puts the original back. A user's later price on the created row blocks the undo for that
// row instead of being deleted with it (#1630).
func TestStretchedSplit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "split-a")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-split',91) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	var orig uuid.UUID
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, start_soc_pct, end_soc_pct, final_state, cost, cost_source, price_per_kwh, created_at, updated_at)
		VALUES ($1,$2,'live_rv-split_c_1',$3,$4,'live',25.8,47,70,'charging_user_stopped',10,'home_rate',0.25,$3,$5) RETURNING id`,
		uid, vid, base, base.Add(20*time.Hour), time.Now().Add(-30*time.Hour)).Scan(&orig); err != nil {
		t.Fatal(err)
	}
	frame := func(at time.Time, soc float64, gear string, speed float64, state, status string, kw float64) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source, battery_level_pct, shift_state, speed_mph, charging_state, charger_status, charger_power_kw, lat, lon)
		      VALUES ($1,$2,$3,'t',$4,$5,$6,$7,$8,$9,34.1,-118.1)`, vid, at, uid, soc, gear, speed, state, status, kw)
	}
	// 0-2 h: plugged and charging 47 -> 55 at 4 kW. 2 h-3 h: parked, plugged (ready).
	for m := 0; m <= 120; m += 10 {
		frame(base.Add(time.Duration(m)*time.Minute), 47+8*float64(m)/120, "P", 0, "charging_active", "chrgr_sts_connected_charging", 4)
	}
	frame(base.Add(150*time.Minute), 55, "P", 0, "charging_ready", "chrgr_sts_connected_no_chrg", 0)
	// 3 h: a 30 min drive, SoC 55 -> 50, unplugged.
	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,'d1',$3,$4,9,'live')`, uid, vid, base.Add(3*time.Hour), base.Add(3*time.Hour+30*time.Minute))
	for m := 0; m <= 30; m += 5 {
		frame(base.Add(3*time.Hour+time.Duration(m)*time.Minute), 55-5*float64(m)/30, "D", 30, "charging_ready", "chrgr_sts_not_connected", 0)
	}
	// 3.5 h-8.5 h: parked and charging again, 50 -> 70 over 5 h at 3.7 kW (18.5 kWh against 18.2 kWh of SoC gain).
	for m := 0; m <= 300; m += 10 {
		frame(base.Add(215*time.Minute+time.Duration(m)*time.Minute), 50+20*float64(m)/300, "P", 0, "charging_active", "chrgr_sts_connected_charging", 3.7)
	}
	frame(base.Add(525*time.Minute), 70, "P", 0, "charging_ready", "chrgr_sts_not_connected", 0)

	rows, err := stretched.Select(ctx, pool, false, true)
	if err != nil {
		t.Fatal(err)
	}
	opt := stretched.Options{Split: true}
	plan := stretched.MakePlan(rows, time.Now(), opt)
	sp := plan.Splits[orig]
	if sp == nil || len(sp.News) != 1 {
		t.Fatalf("want one split with one new row, got cap=%d flagged=%d skipped=%v", len(plan.Cap), len(plan.Flagged), plan.Skipped)
	}
	nr := sp.News[0]
	if nr.KWh < 17 || nr.KWh > 19.5 || nr.SoCStart < 49 || nr.SoCEnd < 69 {
		t.Fatalf("new row = %+v", nr)
	}
	if got := sp.OrigKWh + nr.KWh; got < 25.799 || got > 25.801 {
		t.Fatalf("energy not conserved: %v", got)
	}
	digest := plan.Digest()

	snapshot := func() string {
		var j string
		if err := pool.QueryRowContext(ctx, `SELECT (to_jsonb(c.*) - 'updated_at')::text FROM charges c WHERE id = $1`, orig).Scan(&j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	count := func() int {
		var n int
		_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id=$1`, uid).Scan(&n)
		return n
	}
	before := snapshot()

	batch := uuid.New()
	if n, err := stretched.Apply(ctx, pool, batch, 1, digest, "t", false, opt, func([]phantom.BackupRow) error { return nil }); err != nil || n != 1 {
		t.Fatalf("apply: %d %v", n, err)
	}
	if count() != 2 {
		t.Fatalf("rows after apply: %d, want original + 1 new", count())
	}
	var cost *float64
	var kwh, endSoC float64
	var ext, meta, state string
	_ = pool.QueryRowContext(ctx, `SELECT cost::float8, energy_added_kwh, end_soc_pct, metadata::text, final_state FROM charges WHERE id=$1`, orig).Scan(&cost, &kwh, &endSoC, &meta, &state)
	if cost == nil || *cost < 1.5 || *cost > 2.2 || kwh < 6.5 || kwh > 8.5 || endSoC < 54 || endSoC > 56 || !strings.Contains(meta, "stretched_repair") || state != "charging_complete" {
		t.Fatalf("original after split: cost=%v kwh=%v end soc=%v state=%s meta=%s", cost, kwh, endSoC, state, meta)
	}
	var newID uuid.UUID
	var newCost, newPlace, newNet *string
	var newState string
	var newActive, newMax, newAvg *float64
	if err := pool.QueryRowContext(ctx, `SELECT id, external_id, cost::text, place, network, final_state, active_seconds, max_power_kw, avg_power_kw FROM charges WHERE user_id=$1 AND id<>$2`, uid, orig).Scan(&newID, &ext, &newCost, &newPlace, &newNet, &newState, &newActive, &newMax, &newAvg); err != nil {
		t.Fatal(err)
	}
	if newState != "charging_user_stopped" || newActive == nil || *newActive < 17000 || newMax == nil || *newMax != 3.7 || newAvg == nil || *newAvg < 3.5 || *newAvg > 3.9 {
		t.Fatalf("new row figures: state=%s active=%v max=%v avg=%v", newState, newActive, newMax, newAvg)
	}
	if ext != nr.ExternalID || newCost != nil {
		t.Fatalf("new row: ext=%q cost=%v; want external id %q and no copied price", ext, newCost, nr.ExternalID)
	}
	// A second pass finds nothing more to do.
	if rows, _ := stretched.Select(ctx, pool, false, true); len(stretched.MakePlan(rows, time.Now(), opt).Cap) != 0 {
		t.Fatal("second pass would change more")
	}

	// A user prices the created row: the undo leaves that group alone.
	exec(`UPDATE charges SET cost_source = 'user' WHERE id = $1`, newID)
	if r, s, err := stretched.Restore(ctx, pool, batch); err != nil || r != 0 || s != 1 {
		t.Fatalf("restore with a user-priced created row: %d %d %v, want 0 restored 1 skipped", r, s, err)
	}
	if count() != 2 {
		t.Fatalf("the user-priced row was deleted: %d rows", count())
	}
	// Without the user's price the undo is exact: the created row goes, the original comes back.
	exec(`UPDATE charges SET cost_source = NULL WHERE id = $1`, newID)
	if r, s, err := stretched.Restore(ctx, pool, batch); err != nil || r != 1 || s != 0 {
		t.Fatalf("restore: %d %d %v", r, s, err)
	}
	if got := snapshot(); got != before {
		t.Fatalf("after restore the original differs:\n before %s\n after  %s", before, got)
	}
	if count() != 1 {
		t.Fatalf("rows after restore: %d, want 1", count())
	}
}
