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

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/phantom"
	"github.com/apohor/rivolt/internal/repair/stretched"
	"github.com/apohor/rivolt/internal/settings"
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
	// Mode A never touches a row that has a plugged frame (or charging after its drive): the cap-only digest is unchanged by it.
	if rows, err := stretched.Select(ctx, pool, false, true); err != nil {
		t.Fatal(err)
	} else if with := stretched.MakePlan(rows, time.Now(), stretched.Options{CapBySoC: true}); with.Digest() != digest {
		t.Fatalf("CapBySoC changed the cap-only plan of rows with plugged frames: %s vs %s", with.Digest(), digest)
	}
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
	restored, err := stretched.Restore(ctx, pool, batch)
	if err != nil || restored != 2 {
		t.Fatalf("restore: %d restored %v", restored, err)
	}
	for id, j := range snapshot() {
		if before[id] != j {
			t.Fatalf("after restore row %s differs:\n before %s\n after  %s", id, before[id], j)
		}
	}
	if _, err := stretched.Restore(ctx, pool, batch); err == nil {
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
	// The user's home is where the truck sat, at 25 cents.
	ss := settings.NewFactory(pool).For(uid)
	if err := settings.SetChargingConfig(ctx, ss, settings.ChargingConfig{HomePricePerKWh: 0.25, HomeCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetHomeLocation(ctx, ss, settings.HomeLocation{Set: true, Latitude: 34.1, Longitude: -118.1, Source: settings.HomeSourceCoordinates}); err != nil {
		t.Fatal(err)
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
	opt := stretched.Options{Split: true, Price: stretched.NewPriceFn(ctx, pool)}
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
	if ext != nr.ExternalID {
		t.Fatalf("new row: ext=%q, want external id %q", ext, nr.ExternalID)
	}
	// Priced by the recorder's own function at the user's home rate, and placed: not a copy of the original's price.
	var newPPK *float64
	var newSrc *string
	_ = pool.QueryRowContext(ctx, `SELECT price_per_kwh::float8, cost_source FROM charges WHERE id = $1`, newID).Scan(&newPPK, &newSrc)
	if newCost == nil || newSrc == nil || *newSrc != "home_rate" || newPPK == nil || *newPPK != 0.25 || newPlace == nil || *newPlace != "home" {
		t.Fatalf("new row pricing: cost=%v source=%v ppk=%v place=%v; want home_rate at 0.25, placed home (plan: %+v)", newCost, newSrc, newPPK, newPlace, nr)
	}
	if by := plan.PricedBySource(); by["home_rate"] != 1 {
		t.Fatalf("priced by source = %v", by)
	}
	// A second pass finds nothing more to do.
	if rows, _ := stretched.Select(ctx, pool, false, true); len(stretched.MakePlan(rows, time.Now(), opt).Cap) != 0 {
		t.Fatal("second pass would change more")
	}

	// The undo is all or nothing. Each of these blocks it, changes nothing, and is listed by the restore dry run.
	afterApply := snapshot()
	blocked := func(what, set, unset string) {
		t.Helper()
		exec(set)
		bl, err := stretched.RestoreBlockers(ctx, pool, batch)
		if err != nil || len(bl) == 0 {
			t.Fatalf("%s: restore dry run lists %v %v, want a blocker", what, bl, err)
		}
		if r, err := stretched.Restore(ctx, pool, batch); !errors.Is(err, stretched.ErrRestoreBlocked) || r != 0 {
			t.Fatalf("%s: restore = %d %v, want ErrRestoreBlocked", what, r, err)
		}
		if count() != 2 {
			t.Fatalf("%s: a blocked restore deleted rows: %d left", what, count())
		}
		exec(unset)
		if got := snapshot(); got != afterApply {
			t.Fatalf("%s: a blocked restore changed the original:\n before %s\n after  %s", what, afterApply, got)
		}
	}
	blocked("a user prices the created row", fmt.Sprintf(`UPDATE charges SET cost_source = 'user' WHERE id = '%s'`, newID), fmt.Sprintf(`UPDATE charges SET cost_source = NULL WHERE id = '%s'`, newID))
	blocked("a user prices the original", fmt.Sprintf(`UPDATE charges SET cost_source = 'user' WHERE id = '%s'`, orig), fmt.Sprintf(`UPDATE charges SET cost_source = 'home_rate' WHERE id = '%s'`, orig))
	blocked("a user notes the original", fmt.Sprintf(`UPDATE charges SET notes = 'mine' WHERE id = '%s'`, orig), fmt.Sprintf(`UPDATE charges SET notes = NULL WHERE id = '%s'`, orig))
	blocked("the original's end moved", fmt.Sprintf(`UPDATE charges SET ended_at = ended_at - interval '1 minute' WHERE id = '%s'`, orig), fmt.Sprintf(`UPDATE charges SET ended_at = ended_at + interval '1 minute' WHERE id = '%s'`, orig))

	// A key added to the original's metadata after the apply survives the undo; the repair's own key goes.
	exec(`UPDATE charges SET metadata = metadata || '{"later": 1}'::jsonb WHERE id = $1`, orig)
	if r, err := stretched.Restore(ctx, pool, batch); err != nil || r != 1 {
		t.Fatalf("restore: %d %v", r, err)
	}
	var metaAfter string
	_ = pool.QueryRowContext(ctx, `SELECT metadata::text FROM charges WHERE id = $1`, orig).Scan(&metaAfter)
	if strings.Contains(metaAfter, "stretched_repair") || !strings.Contains(metaAfter, `"later"`) {
		t.Fatalf("metadata after restore = %s, want the repair's key gone and the later key kept", metaAfter)
	}
	exec(`UPDATE charges SET metadata = metadata - 'later' WHERE id = $1`, orig)
	if got := snapshot(); got != before {
		t.Fatalf("after restore the original differs:\n before %s\n after  %s", before, got)
	}
	if count() != 1 {
		t.Fatalf("rows after restore: %d, want 1", count())
	}
}

// The split's pricing step: AC at home gets the home rate and place "home"; AC away from home is placed "away" and left
// unpriced; a DC fast charge is left alone; a user without a home location keeps the home rate (the recorder's rule).
func TestStretchedPriceFn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "price-a")
	if err != nil {
		t.Fatal(err)
	}
	ss := settings.NewFactory(pool).For(uid)
	if err := settings.SetChargingConfig(ctx, ss, settings.ChargingConfig{HomePricePerKWh: 0.30, HomeCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	price := stretched.NewPriceFn(ctx, pool)
	t0 := time.Now().UTC().Add(-48 * time.Hour)
	mk := func(lat, lon, kw, kwh float64, hours float64) charges.Charge {
		return charges.Charge{StartedAt: t0, EndedAt: t0.Add(time.Duration(hours * float64(time.Hour))), Lat: lat, Lon: lon, MaxPowerKW: kw, ActiveSeconds: hours * 3600, EnergyAddedKWh: kwh}
	}
	// No home location saved: slow charging keeps the home rate, no place.
	c := mk(34.1, -118.1, 7, 14, 2)
	price(uid, &c)
	if c.CostSource != "home_rate" || c.PricePerKWh != 0.30 || c.Place != "" || c.Cost < 4.19 || c.Cost > 4.21 {
		t.Fatalf("no home saved: %+v", c)
	}
	if err := settings.SetHomeLocation(ctx, ss, settings.HomeLocation{Set: true, Latitude: 34.1, Longitude: -118.1, Source: settings.HomeSourceCoordinates}); err != nil {
		t.Fatal(err)
	}
	// At home: home rate, placed home. Away: placed away, unpriced. The memo is fresh per row (no leaked state).
	home := mk(34.1, -118.1, 7, 14, 2)
	price(uid, &home)
	away := mk(40.0, -100.0, 7, 14, 2)
	price(uid, &away)
	if home.CostSource != "home_rate" || home.Place != "home" || away.Cost != 0 || away.CostSource != "" || away.Place != "away" {
		t.Fatalf("home %+v away %+v", home, away)
	}
	// A DC fast charge is left alone (the repair has no archive or detection; `rivolt reprice --detect` does that).
	dc := mk(34.1, -118.1, 150, 40, 0.4)
	price(uid, &dc)
	if dc.Cost != 0 || dc.CostSource != "" || dc.Place != "" {
		t.Fatalf("a DC fast charge must be left unpriced and unplaced: %+v", dc)
	}
}

// A stretched row whose only charging came after its first drive (nothing plugged before it, SoC flat): with --split the
// original is kept as a zero-energy abandoned row ended at the first drive, the real charge becomes its own priced row, and
// the undo returns the original exactly and deletes the created row (#1630).
func TestStretchedSplitEmptyOriginal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "split-empty")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-empty',91) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ss := settings.NewFactory(pool).For(uid)
	if err := settings.SetChargingConfig(ctx, ss, settings.ChargingConfig{HomePricePerKWh: 0.25, HomeCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetHomeLocation(ctx, ss, settings.HomeLocation{Set: true, Latitude: 34.1, Longitude: -118.1, Source: settings.HomeSourceCoordinates}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	var orig uuid.UUID
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, start_soc_pct, end_soc_pct, final_state, cost, cost_source, price_per_kwh, created_at, updated_at)
		VALUES ($1,$2,'live_rv-empty_c_1',$3,$4,'live',18.6,50,70,'charging_user_stopped',4.65,'home_rate',0.25,$3,$5) RETURNING id`,
		uid, vid, base, base.Add(20*time.Hour), time.Now().Add(-30*time.Hour)).Scan(&orig); err != nil {
		t.Fatal(err)
	}
	frame := func(at time.Time, soc float64, gear string, speed float64, state, status string, kw float64) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source, battery_level_pct, shift_state, speed_mph, charging_state, charger_status, charger_power_kw, lat, lon)
		      VALUES ($1,$2,$3,'t',$4,$5,$6,$7,$8,$9,34.1,-118.1)`, vid, at, uid, soc, gear, speed, state, status, kw)
	}
	// Before the drive: parked, never plugged, SoC flat at 50.
	for m := 0; m <= 120; m += 30 {
		frame(base.Add(time.Duration(m)*time.Minute), 50, "P", 0, "charging_ready", "chrgr_sts_not_connected", 0)
	}
	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,'d1',$3,$4,9,'live')`, uid, vid, base.Add(3*time.Hour), base.Add(3*time.Hour+30*time.Minute))
	for m := 0; m <= 30; m += 5 {
		frame(base.Add(3*time.Hour+time.Duration(m)*time.Minute), 50, "D", 30, "charging_ready", "chrgr_sts_not_connected", 0)
	}
	// After it: parked and charging, 50 -> 70 over 5 h at 3.7 kW.
	for m := 0; m <= 300; m += 10 {
		frame(base.Add(215*time.Minute+time.Duration(m)*time.Minute), 50+20*float64(m)/300, "P", 0, "charging_active", "chrgr_sts_connected_charging", 3.7)
	}

	opt := stretched.Options{Split: true, Price: stretched.NewPriceFn(ctx, pool)}
	rows, err := stretched.Select(ctx, pool, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if plain := stretched.MakePlan(rows, time.Now(), stretched.Options{}); len(plain.Cap) != 0 || plain.Skipped["no plugged frame inside"] != 1 {
		t.Fatalf("without --split the row stays skipped: %v", plain.Skipped)
	}
	plan := stretched.MakePlan(rows, time.Now(), opt)
	sp := plan.Splits[orig]
	if sp == nil || !sp.EmptyOriginal || len(sp.News) != 1 || plan.EmptyOriginals() != 1 {
		t.Fatalf("want one split with an empty original, got cap=%d flagged=%d skipped=%v", len(plan.Cap), len(plan.Flagged), plan.Skipped)
	}
	snap := func() string {
		var j string
		if err := pool.QueryRowContext(ctx, `SELECT (to_jsonb(c.*) - 'updated_at')::text FROM charges c WHERE id = $1`, orig).Scan(&j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	before := snap()
	batch := uuid.New()
	if n, err := stretched.Apply(ctx, pool, batch, 1, plan.Digest(), "t", false, opt, func([]phantom.BackupRow) error { return nil }); err != nil || n != 1 {
		t.Fatalf("apply: %d %v", n, err)
	}
	var state string
	var ended time.Time
	var kwh float64
	if err := pool.QueryRowContext(ctx, `SELECT final_state, ended_at, energy_added_kwh FROM charges WHERE id = $1`, orig).Scan(&state, &ended, &kwh); err != nil {
		t.Fatal(err)
	}
	if state != "abandoned" || !ended.Equal(base.Add(3*time.Hour)) || kwh > 0.5 {
		t.Fatalf("empty original after apply: state=%s ended=%v (want %v) kwh=%v", state, ended, base.Add(3*time.Hour), kwh)
	}
	var newKWh float64
	var src, place *string
	if err := pool.QueryRowContext(ctx, `SELECT energy_added_kwh, cost_source, place FROM charges WHERE user_id=$1 AND id<>$2`, uid, orig).Scan(&newKWh, &src, &place); err != nil {
		t.Fatal(err)
	}
	if got := kwh + newKWh; got < 18.599 || got > 18.601 || src == nil || *src != "home_rate" || place == nil || *place != "home" {
		t.Fatalf("new row: kwh %v (sum %v) source %v place %v", newKWh, kwh+newKWh, src, place)
	}
	if r, err := stretched.Restore(ctx, pool, batch); err != nil || r != 1 {
		t.Fatalf("restore: %d %v", r, err)
	}
	if got := snap(); got != before {
		t.Fatalf("after restore the original differs:\n before %s\n after  %s", before, got)
	}
	var n int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE user_id = $1`, uid).Scan(&n)
	if n != 1 {
		t.Fatalf("rows after restore: %d, want 1", n)
	}
}

// Mode A against Postgres: a status-less truck (no charger status, no power) charged, then drove, and the row ran on. It is
// capped at the last SoC rise before the drive, only with CapBySoC; the undo returns it exactly. With the mode off the plan
// does not see it, and the plugged-frame plan of the same data is unchanged (#1630).
func TestStretchedCapBySoC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "cap-soc")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-soc',135) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	var id uuid.UUID
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, start_soc_pct, end_soc_pct, final_state, created_at, updated_at)
		VALUES ($1,$2,'live_rv-soc_c_1',$3,$4,'live',27,40,60,'abandoned',$3,$5) RETURNING id`,
		uid, vid, base, base.Add(20*time.Hour), time.Now().Add(-30*time.Hour)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// No charger status, no power: the SoC rises 40 -> 60 over 2 h (every 15 min), stays at 60, then a drive at 4 h.
	frame := func(at time.Time, soc float64, gear string, speed float64) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source, battery_level_pct, shift_state, speed_mph) VALUES ($1,$2,$3,'t',$4,$5,$6)`, vid, at, uid, soc, gear, speed)
	}
	for m := 0; m <= 120; m += 15 {
		frame(base.Add(time.Duration(m)*time.Minute), 40+20*float64(m)/120, "P", 0)
	}
	for m := 150; m <= 210; m += 30 {
		frame(base.Add(time.Duration(m)*time.Minute), 60, "P", 0)
	}
	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,'d1',$3,$4,9,'live')`, uid, vid, base.Add(4*time.Hour), base.Add(4*time.Hour+30*time.Minute))
	for m := 0; m <= 30; m += 10 {
		frame(base.Add(4*time.Hour+time.Duration(m)*time.Minute), 60-float64(m)/10, "D", 30)
	}

	sel := func(frames bool) []stretched.Row {
		rows, err := stretched.Select(ctx, pool, false, frames)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if p := stretched.MakePlan(sel(true), time.Now(), stretched.Options{}); len(p.Cap) != 0 || p.Skipped["no plugged frame inside"] != 1 {
		t.Fatalf("mode off: the row must stay skipped: cap=%d %v", len(p.Cap), p.Skipped)
	}
	opt := stretched.Options{CapBySoC: true}
	plan := stretched.MakePlan(sel(true), time.Now(), opt)
	if len(plan.Cap) != 1 || !plan.BySoC[id] || !plan.CapTo[id].Equal(base.Add(2*time.Hour)) {
		t.Fatalf("want a mode-A cap at the last SoC rise (2 h), got cap=%d to=%v skipped=%v", len(plan.Cap), plan.CapTo[id], plan.Skipped)
	}
	if n, off := plan.SoCModeTotals(); n != 1 || off != 0 {
		t.Fatalf("totals %d %d (27 kWh stored against 20 points of a 135 kWh pack agree)", n, off)
	}
	snap := func() string {
		var j string
		if err := pool.QueryRowContext(ctx, `SELECT (to_jsonb(c.*) - 'updated_at')::text FROM charges c WHERE id = $1`, id).Scan(&j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	before := snap()
	batch := uuid.New()
	if n, err := stretched.Apply(ctx, pool, batch, 1, plan.Digest(), "t", false, opt, func([]phantom.BackupRow) error { return nil }); err != nil || n != 1 {
		t.Fatalf("apply: %d %v", n, err)
	}
	var ended time.Time
	var kwh float64
	var endSoC float64
	_ = pool.QueryRowContext(ctx, `SELECT ended_at, energy_added_kwh, end_soc_pct FROM charges WHERE id = $1`, id).Scan(&ended, &kwh, &endSoC)
	if !ended.Equal(base.Add(2*time.Hour)) || kwh != 27 || endSoC != 60 {
		t.Fatalf("after apply: ended=%v kwh=%v soc=%v; want the cap at 2 h and energy and SoC untouched", ended, kwh, endSoC)
	}
	if r, err := stretched.Restore(ctx, pool, batch); err != nil || r != 1 {
		t.Fatalf("restore: %d %v", r, err)
	}
	if got := snap(); got != before {
		t.Fatalf("after restore the row differs:\n before %s\n after  %s", before, got)
	}
}
