//go:build integration

// /now state derivation against real Postgres with every migration.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

func TestNowSnapshot_StateRunSleepsAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "now-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "now-bob")
	if err != nil {
		t.Fatal(err)
	}
	var va, vb string
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-now') RETURNING id`, alice).Scan(&va); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-bob') RETURNING id`, bob).Scan(&vb); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	row := func(uid, vid string, ago time.Duration, power, shift, charge string, soc, kw float64) {
		t.Helper()
		if _, err := pool.Exec(`INSERT INTO vehicle_state
			(user_id, vehicle_id, at, source, power_state, shift_state, charging_state, battery_level_pct, charger_power_kw, tire_pressure_min_bar, location_fix_at)
			VALUES ($1,$2,$3,'live',$4,$5,$6,$7,$8,2.6,$3)`,
			uid, vid, now.Add(-ago), power, shift, charge, soc, kw); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Alice: parked, a 30 s failed sleep, then plugged and charging.
	row(alice.String(), va, 120*time.Minute, "ready", "P", "charger_disconnected", 60, 0)
	row(alice.String(), va, 90*time.Minute, "sleep", "P", "charger_disconnected", 60, 0)
	row(alice.String(), va, 90*time.Minute-30*time.Second, "ready", "P", "charger_disconnected", 60, 0)
	row(alice.String(), va, 40*time.Minute, "ready", "P", "charging_active", 41, 11)
	row(alice.String(), va, 20*time.Minute, "ready", "P", "charging_active", 55, 150)
	row(alice.String(), va, 1*time.Minute, "ready", "P", "charging_active", 70, 120)
	// Bob's row must not leak into Alice's answer.
	row(bob.String(), vb, 5*time.Second, "ready", "D", "charger_disconnected", 10, 0)

	snap, err := newNowSnapshotter(pool)(ctx, alice, "rv-now", now)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != "charging" {
		t.Fatalf("state = %q", snap.State)
	}
	if snap.StateSince == nil || !snap.StateSince.Equal(now.Add(-40*time.Minute)) {
		t.Fatalf("state_since = %v", snap.StateSince)
	}
	if snap.SoCAtStateStart == nil || *snap.SoCAtStateStart != 41 {
		t.Fatalf("soc start = %v", snap.SoCAtStateStart)
	}
	if snap.ChargePeakKW == nil || *snap.ChargePeakKW != 150 {
		t.Fatalf("peak = %v", snap.ChargePeakKW)
	}
	if snap.FailedSleeps6h != 1 {
		t.Fatalf("failed sleeps = %d", snap.FailedSleeps6h)
	}
	if snap.TirePressureMinB == nil || snap.LocationFixAt == nil {
		t.Fatalf("tire/fix missing: %+v", snap)
	}
	// A state that has not changed inside the 7-day window has an
	// unknown start: state_since is NULL, not the window edge.
	var vold string
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-old') RETURNING id`, alice).Scan(&vold); err != nil {
		t.Fatal(err)
	}
	row(alice.String(), vold, 5*24*time.Hour, "ready", "P", "charger_disconnected", 80, 0)
	row(alice.String(), vold, 1*time.Hour, "ready", "P", "charger_disconnected", 79, 0)
	old, err := newNowSnapshotter(pool)(ctx, alice, "rv-old", now)
	if err != nil {
		t.Fatal(err)
	}
	if old.State != "parked" || old.StateSince != nil {
		t.Fatalf("old run: state=%q since=%v", old.State, old.StateSince)
	}
	// Plugged outranks asleep.
	row(alice.String(), vold, 30*time.Minute, "sleep", "P", "charging_complete", 79, 0)
	pl, err := newNowSnapshotter(pool)(ctx, alice, "rv-old", now)
	if err != nil {
		t.Fatal(err)
	}
	if pl.State != "plugged" || !pl.Asleep {
		t.Fatalf("plugged+asleep = %q asleep=%v, want plugged/true", pl.State, pl.Asleep)
	}
	// Plugged and awake: same state, asleep false.
	row(alice.String(), vold, 10*time.Minute, "ready", "P", "charging_complete", 79, 0)
	pa, err := newNowSnapshotter(pool)(ctx, alice, "rv-old", now)
	if err != nil {
		t.Fatal(err)
	}
	if pa.State != "plugged" || pa.Asleep {
		t.Fatalf("plugged+awake = %q asleep=%v, want plugged/false", pa.State, pa.Asleep)
	}
	// charger_status decides "plugged" when the row has one: an R2 reports
	// charging_ready while parked unplugged.
	for _, c := range []struct {
		name, status, charge, want string
		model                      string
		kw                         float64
	}{
		{name: "rv-cs-unplugged", status: "chrgr_sts_not_connected", charge: "charging_ready", want: "parked"},
		{name: "rv-cs-connected-idle", status: "chrgr_sts_connected_no_chrg", charge: "charging_ready", want: "plugged"},
		{name: "rv-cs-connected-complete", status: "chrgr_sts_connected_no_chrg", charge: "charging_complete", want: "plugged"},
		{name: "rv-cs-legacy-null", charge: "charging_ready", want: "plugged"}, // R1 without charger_status: old rule
		{name: "rv-cs-unplugged-complete", status: "chrgr_sts_not_connected", charge: "charging_complete", want: "parked"},
		// R2: never reports charger_status; charging_ready/complete say
		// nothing about the plug, only charging_connecting with power does.
		{name: "rv-r2-ready", model: "R2", charge: "charging_ready", want: "parked"},
		{name: "rv-r2-ready-power", model: "R2", charge: "charging_ready", kw: 7, want: "parked"},
		{name: "rv-r2-complete", model: "R2", charge: "charging_complete", want: "parked"},
		{name: "rv-r2-connecting-idle", model: "R2", charge: "charging_connecting", want: "parked"},
		{name: "rv-r2-connecting-power", model: "R2", charge: "charging_connecting", kw: 11, want: "plugged"},
		{name: "rv-r2-status-connected", model: "R2", status: "chrgr_sts_connected_no_chrg", charge: "charging_ready", want: "plugged"},
		// connected means plugged whatever the charging_state says
		{name: "rv-cs-connected-waiting", status: "chrgr_sts_connected_no_chrg", charge: "charging_scheduled", want: "plugged"},
		{name: "rv-cs-connected-waiting2", status: "chrgr_sts_connected_no_chrg", charge: "waiting_on_charger", want: "plugged"},
		{name: "rv-cs-notconnected-waiting", status: "chrgr_sts_not_connected", charge: "waiting_on_charger", want: "parked"},
		{name: "rv-r1-ready-model", model: "R1T", charge: "charging_ready", want: "plugged"},
	} {
		var vid string
		if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,$2,NULLIF($3,'')) RETURNING id`, alice, c.name, c.model).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		var cs any
		if c.status != "" {
			cs = c.status
		}
		if _, err := pool.Exec(`INSERT INTO vehicle_state
			(user_id, vehicle_id, at, source, power_state, shift_state, charging_state, charger_status, battery_level_pct, charger_power_kw, location_fix_at)
			VALUES ($1,$2,$3,'live','ready','P',$4,$5,50,$6,$3)`, alice, vid, now.Add(-time.Minute), c.charge, cs, c.kw); err != nil {
			t.Fatalf("seed %s: %v", c.name, err)
		}
		got, err := newNowSnapshotter(pool)(ctx, alice, c.name, now)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, got.State, c.want)
		}
	}
	// Not Alice's vehicle: no data, state empty.
	other, err := newNowSnapshotter(pool)(ctx, alice, "rv-bob", now)
	if err != nil {
		t.Fatal(err)
	}
	if other.State != "" || other.At != nil {
		t.Fatalf("leak: %+v", other)
	}
}

// The run query must stay inside the lookback: its plan may only touch
// vehicle_state partitions that overlap [now - nowLookback, ∞). Before the
// fix it joined the break CTE, so every partition was read for the
// vehicle's whole history on every cache miss.
func TestNowRunSQL_PrunesPartitionsOutsideTheLookback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "now-prune")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-prune')`, uid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// An old partition well outside the lookback, so there is something to prune.
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, now.AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	lookback := now.Add(-nowLookback)
	rows, err := pool.QueryContext(ctx, `
		SELECT c.relname
		  FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'vehicle_state'::regclass
		   AND (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'TO \(''([^'']+)''\)'))[1]::timestamptz <= $1`, lookback)
	if err != nil {
		t.Fatal(err)
	}
	var outside []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		outside = append(outside, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(outside) == 0 {
		t.Fatal("no partition outside the lookback to check against")
	}
	var plan string
	if err := pool.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+nowRunSQL, uid, "rv-prune", lookback, "parked").Scan(&plan); err != nil {
		t.Fatalf("explain: %v", err)
	}
	for _, p := range outside {
		if strings.Contains(plan, `"`+p+`"`) {
			t.Errorf("the run query's plan reads %s, a partition entirely before the lookback", p)
		}
	}
}
