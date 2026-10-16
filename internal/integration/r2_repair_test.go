//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/r2"
)

// TestR2HistoryRepair: a never-closed live drive spanning two trips and
// a charge is replaced by the two trips, the charge is added, the
// drive's recap is backed up and removed; restore puts it all back.
func TestR2HistoryRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "r2-repair")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, pack_kwh) VALUES ($1,'rv-r2','R2',91.5) RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Minute)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	sample := func(min int, power, cs string, odo, soc, kw float64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO vehicle_state (vehicle_id, at, user_id, power_state, charging_state, odometer_mi, battery_level_pct, speed_mph, lat, lon, charger_power_kw)
			VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,0,34.1,-118.1,$8)`, vid, at(min), uid, power, cs, odo, soc, kw); err != nil {
			t.Fatalf("sample %d: %v", min, err)
		}
	}
	// Trip 1 (0–10), parked, trip 2 (30–45), parked, a charge (60–120).
	for m := 0; m <= 10; m += 2 {
		sample(m, "go", "", 1000+float64(m)*0.5, 60-float64(m)*0.1, 0)
	}
	sample(14, "ready", "", 1005, 59, 0)
	sample(20, "sleep", "", 1005, 59, 0)
	for m := 30; m <= 45; m += 3 {
		sample(m, "go", "", 1005+float64(m-30)*0.4, 59-float64(m-30)*0.1, 0)
	}
	sample(50, "ready", "", 1011, 57.5, 0)
	for m := 60; m <= 120; m += 5 {
		sample(m, "ready", "charging_active", 1011, 57.5+float64(m-60)*0.1, 7.4)
	}
	sample(125, "ready", "charging_complete", 1011, 63.5, 0)

	// The never-closed drive the recorder left, with a recap.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source)
		VALUES ($1,$2,'live_rv-r2_d_old',$3,$4,11,'live')`, uid, vid, at(0), at(130)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO drive_recaps (user_id, drive_id, model, recap) VALUES ($1,'live_rv-r2_d_old','m','old recap')`, uid); err != nil {
		t.Fatal(err)
	}

	v, err := r2.LoadVehicle(ctx, pool, "rv-r2")
	if err != nil {
		t.Fatal(err)
	}
	since, until := at(-1), at(140)
	rows, _ := r2.LoadRows(ctx, pool, v, since, until)
	drives, _ := r2.LoadDrives(ctx, pool, v, since, until)
	stored, _ := r2.LoadCharges(ctx, pool, v, since, until)
	trips, chs := r2.Segment(rows)
	plan, err := r2.MakePlan(v, since, until, trips, chs, drives, stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DeleteDrives) != 1 || len(plan.InsertDrives) != 2 || len(plan.InsertCharges) != 1 {
		t.Fatalf("plan: delete %d insert %d charges %d", len(plan.DeleteDrives), len(plan.InsertDrives), len(plan.InsertCharges))
	}
	var backup []r2.BackupRow
	sink := func(rs []r2.BackupRow) error { backup = append(backup, rs...); return nil }
	if err := r2.Apply(ctx, pool, plan, func([][2]float64) string { return "poly" }, sink); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM drives WHERE vehicle_id=$1`, vid); n != 2 {
		t.Errorf("drives after apply: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM drives WHERE vehicle_id=$1 AND metadata->>'repair' = 'r2-history' AND route_polyline = 'poly'`, vid); n != 2 {
		t.Errorf("repaired drives marked with a route: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM charges WHERE vehicle_id=$1 AND energy_added_kwh > 6`, vid); n != 1 {
		t.Errorf("charges after apply: %d, want 1 (~7.4 kWh)", n)
	}
	if n := count(`SELECT count(*) FROM drive_recaps WHERE user_id=$1`, uid); n != 0 {
		t.Errorf("the replaced drive's recap survived: %d", n)
	}
	kinds := map[string]int{}
	for _, r := range backup {
		kinds[r.Kind]++
	}
	if kinds["drive"] != 1 || kinds["drive_recaps"] != 1 || kinds["inserted_drive"] != 2 || kinds["inserted_charge"] != 1 {
		t.Fatalf("backup kinds %v", kinds)
	}

	// Running the same plan again fails cleanly: the planned drive is gone.
	if err := r2.Apply(ctx, pool, plan, func([][2]float64) string { return "" }, func([]r2.BackupRow) error { return nil }); err == nil {
		t.Error("a stale plan applied twice")
	}

	// A repaired trip the user annotated since: restore refuses.
	if _, err := pool.ExecContext(ctx, `UPDATE drives SET notes = 'mine' WHERE id = (SELECT id FROM drives WHERE vehicle_id = $1 ORDER BY started_at LIMIT 1)`, vid); err != nil {
		t.Fatal(err)
	}
	if err := r2.Restore(ctx, pool, v, backup); err == nil {
		t.Fatal("restore removed a repaired drive the user had annotated")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE drives SET notes = NULL WHERE vehicle_id = $1`, vid); err != nil {
		t.Fatal(err)
	}
	if err := r2.Restore(ctx, pool, v, backup); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM drives WHERE vehicle_id=$1 AND external_id='live_rv-r2_d_old'`, vid); n != 1 {
		t.Error("restore didn't bring the old drive back")
	}
	if n := count(`SELECT count(*) FROM drives WHERE vehicle_id=$1`, vid); n != 1 {
		t.Errorf("drives after restore: %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM charges WHERE vehicle_id=$1`, vid); n != 0 {
		t.Errorf("charges after restore: %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM drive_recaps WHERE user_id=$1 AND recap='old recap'`, uid); n != 1 {
		t.Error("restore didn't bring the recap back")
	}
}
