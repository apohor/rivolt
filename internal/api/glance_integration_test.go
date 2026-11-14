//go:build integration

package api

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// The glance row is the newest stored row of this user's vehicle, range in km, with the state the Now card uses; another user's row,
// and rows older than the lookback, never answer.
func TestGlanceRowFromStoredState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, _ := db.EnsureUser(ctx, pool, "glance-alice")
	bob, _ := db.EnsureUser(ctx, pool, "glance-bob")
	var va, vb string
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-glance') RETURNING id`, alice).Scan(&va); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-glance-bob') RETURNING id`, bob).Scan(&vb); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	ins := func(uid, vid string, ago time.Duration, soc, mi, kw float64, charge string) {
		t.Helper()
		if _, err := pool.Exec(`INSERT INTO vehicle_state (user_id, vehicle_id, at, source, power_state, shift_state, charging_state, battery_level_pct, range_mi, charge_limit_pct, charger_power_kw)
			VALUES ($1,$2,$3,'live','ready','P',$4,$5,$6,80,$7)`, uid, vid, now.Add(-ago), charge, soc, mi, kw); err != nil {
			t.Fatal(err)
		}
	}
	ins(alice.String(), va, 10*time.Minute, 50, 150, 0, "charger_disconnected")
	ins(alice.String(), va, 1*time.Minute, 60, 200, 11, "charging_active")
	ins(bob.String(), vb, 5*time.Second, 5, 10, 0, "charger_disconnected")

	row := newGlanceRow(pool)
	got, err := row(ctx, alice, "rv-glance", now.Add(-glanceLookback))
	if err != nil || got == nil {
		t.Fatalf("row = %v, %v", got, err)
	}
	if *got.BatteryLevelPct != 60 || math.Abs(*got.DistanceToEmpty-200*1.609344) > 1e-6 || *got.ChargeTargetPct != 80 || *got.ChargerPowerKW != 11 ||
		got.ChargerState != "charging_active" || got.State != "charging" || got.Gear != "P" {
		t.Errorf("row = %+v", got)
	}
	if got, err := row(ctx, alice, "rv-glance", now.Add(-30*time.Second)); err != nil || got != nil {
		t.Errorf("a window newer than every row gave %v, %v", got, err)
	}
	if got, err := row(ctx, alice, "rv-glance-bob", now.Add(-time.Hour)); err != nil || got != nil {
		t.Errorf("another user's vehicle answered: %v, %v", got, err)
	}
}
