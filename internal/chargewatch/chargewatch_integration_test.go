//go:build integration

package chargewatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/chargewatch"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The open live rows are split by what the vehicle's latest frame says, with the janitor's rule, and aged from the row's last write; closed rows
// and other sources are not counted.
func TestMeasureSplitsOpenRowsByTheLatestFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "chargewatch-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	mkVehicle := func(name string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, name+uuid.NewString()[:6]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mkCharge := func(v uuid.UUID, state, source string, ended time.Time) {
		if _, err := pool.ExecContext(ctx, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, final_state, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, uid, v, uuid.NewString(), ended.Add(-time.Hour), ended, state, source); err != nil {
			t.Fatal(err)
		}
	}
	mkFrame := func(v uuid.UUID, at time.Time, charging, status, gear string, kw float64) {
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (vehicle_id, at, user_id, source, charging_state, charger_status, charger_power_kw, shift_state)
			VALUES ($1, $2, $3, 'live', $4, $5, $6, $7)`, v, at, uid, charging, status, kw, gear); err != nil {
			t.Fatal(err)
		}
	}
	base, err := chargewatch.Measure(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}

	still := mkVehicle("still") // plugged and charging: charging
	mkCharge(still, "charging_active", "live", now.Add(-10*time.Minute))
	mkFrame(still, now.Add(-1*time.Minute), "charging_active", "chrgr_sts_connected_charging", "P", 7.2)

	stuck := mkVehicle("stuck") // the charger state stuck after an unplug, then a drive: not charging, and old
	mkCharge(stuck, "charging_ready", "live", now.Add(-3*time.Hour))
	mkFrame(stuck, now.Add(-2*time.Hour), "charging_ready", "chrgr_sts_connected_no_chrg", "P", 0)
	mkFrame(stuck, now.Add(-1*time.Hour), "charging_ready", "chrgr_sts_not_connected", "D", 0)

	silent := mkVehicle("silent") // no frame at all in the window: counted apart, not as a stuck charge
	mkCharge(silent, "charging_active", "live", now.Add(-5*time.Hour))

	closed := mkVehicle("closed") // a closed row and a non-live one are not open rows
	mkCharge(closed, "charging_complete", "live", now.Add(-9*time.Hour))
	mkCharge(closed, "charging_active", "electrafi_import", now.Add(-9*time.Hour))

	got, err := chargewatch.Measure(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Charging - base.Charging; d != 1 {
		t.Errorf("charging grew by %d, want 1", d)
	}
	if d := got.NotCharging - base.NotCharging; d != 1 {
		t.Errorf("not_charging grew by %d, want 1 (the stuck row only: a vehicle with no recent frame is dormant, not stuck)", d)
	}
	if d := got.NoFrame - base.NoFrame; d != 1 {
		t.Errorf("no_frame grew by %d, want 1", d)
	}
	if got.OldestNotChargingSec < (3 * time.Hour).Seconds() {
		t.Errorf("oldest not_charging = %.0f s, want at least 3 h (the stuck row, from its last write)", got.OldestNotChargingSec)
	}
	if got.OldestNoFrameSec < (5 * time.Hour).Seconds() {
		t.Errorf("oldest no_frame = %.0f s, want at least 5 h", got.OldestNoFrameSec)
	}
	if got.OldestChargingSec < (10 * time.Minute).Seconds() {
		t.Errorf("oldest charging = %.0f s, want at least 10 min", got.OldestChargingSec)
	}
}
