//go:build integration

package samples_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/testpg"
)

// A guarded sample insert (fence token in the context) is written only while the group lease is
// valid, in the same statement; every other case is ErrFenced and writes nothing.
func TestInsertBatch_FencedByTheGroupLease(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "fence-test")
	if err != nil {
		t.Fatal(err)
	}
	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	var epoch int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	tok := fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epoch}
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	mk := func(i int) samples.Sample {
		return samples.Sample{VehicleID: "rv-fence", At: at.Add(time.Duration(i) * time.Second), BatteryLevelPct: 50,
			Source: "live", ShiftState: "P", PowerState: "go", ChargerStatus: "chrgr_sts_not_connected"}
	}
	count := func() int {
		n, err := store.Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The current token writes, and a duplicate is not mistaken for a refusal.
	if err := store.InsertBatch(fence.With(ctx, tok), []samples.Sample{mk(0)}); err != nil {
		t.Fatalf("guarded insert with a valid lease: %v", err)
	}
	if err := store.InsertBatch(fence.With(ctx, tok), []samples.Sample{mk(0)}); err != nil {
		t.Fatalf("a duplicate sample under a valid lease: %v", err)
	}
	if n := count(); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}

	// A stale epoch, another pod, and an unknown group are all refused.
	for name, bad := range map[string]fence.Token{
		"stale epoch": {Group: tok.Group, Pod: "pod-a", Epoch: epoch - 1},
		"other pod":   {Group: tok.Group, Pod: "pod-b", Epoch: epoch},
		"no lease":    {Group: "00000000-0000-0000-0000-000000000001", Pod: "pod-a", Epoch: epoch},
	} {
		if err := store.InsertBatch(fence.With(ctx, bad), []samples.Sample{mk(1)}); !errors.Is(err, fence.ErrFenced) {
			t.Errorf("%s: err = %v, want ErrFenced", name, err)
		}
	}
	if n := count(); n != 1 {
		t.Fatalf("a refused write left %d rows, want 1", n)
	}

	// An expired lease that nobody has taken is refused too (statement_timestamp, not a steal).
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() - interval '1 second' WHERE group_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertBatch(fence.With(ctx, tok), []samples.Sample{mk(2)}); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("expired lease: err = %v, want ErrFenced", err)
	}
	// A taken lease (new epoch, other pod): the old token is refused, the new one writes.
	var e2 int64
	if err := pool.QueryRowContext(ctx, `UPDATE account_leases SET pod_id = 'pod-b', epoch = nextval('account_lease_epoch'), expires_at = now() + interval '30 seconds' WHERE group_id = $1 RETURNING epoch`, uid).Scan(&e2); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertBatch(fence.With(ctx, tok), []samples.Sample{mk(3)}); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner after a steal: err = %v, want ErrFenced", err)
	}
	if err := store.InsertBatch(fence.With(ctx, fence.Token{Group: tok.Group, Pod: "pod-b", Epoch: e2}), []samples.Sample{mk(3)}); err != nil {
		t.Errorf("new owner: %v", err)
	}
	if n := count(); n != 2 {
		t.Fatalf("%d rows, want 2 (the first, and the new owner's)", n)
	}
	// An unguarded write (no token: imports, repairs) is unchanged.
	if err := store.InsertBatch(ctx, []samples.Sample{mk(4)}); err != nil {
		t.Errorf("unguarded: %v", err)
	}
}

// A transaction that spans the lease expiry: the statement before it commits to the transaction,
// the statement after it is refused, and the transaction is rolled back (9a of the design).
func TestGuardedTransactionSpanningTheExpiryIsRefused(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "fence-span")
	if err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '1 second')
		RETURNING epoch`, uid).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	tx, tctx, tcancel, err := fence.BeginBounded(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer tcancel()
	defer func() { _ = tx.Rollback() }()
	var ok bool
	if err := tx.QueryRowContext(tctx, `SELECT rivolt_lease_ok($1::uuid, 'pod-a', $2)`, uid, epoch).Scan(&ok); err != nil || !ok {
		t.Fatalf("before the expiry: ok=%v err=%v", ok, err)
	}
	// The transaction keeps working (a running statement, so not idle) past the 1 s expiry: the next
	// guarded statement, which starts after it, is refused although the transaction began before it.
	if _, err := tx.ExecContext(tctx, `SELECT pg_sleep(1.3)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(tctx, `SELECT rivolt_lease_ok($1::uuid, 'pod-a', $2)`, uid, epoch).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a statement after the expiry, in a transaction that began before it, was accepted")
	}
}

// An idle guarded transaction is ended by the server after 1 s, so it cannot hold a write open
// across a steal.
func TestGuardedTransactionIdleTimeout(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, tctx, tcancel, err := fence.BeginBounded(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer tcancel()
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(tctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1300 * time.Millisecond)
	if _, err := tx.ExecContext(tctx, `SELECT 1`); err == nil {
		t.Error("an idle guarded transaction survived past the 1 s idle-in-transaction timeout")
	}
}

// The owner's catch-up flush (InsertBulk with a token) is one guarded statement per chunk: written only
// while the lease holds, identical rows to the plain path, and a stale owner writes nothing.
func TestInsertBulk_FencedByTheGroupLease(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "bulkfence")
	if err != nil {
		t.Fatal(err)
	}
	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	var epoch int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	alt, tire, temp := 150.0, 2.6, 31.5
	fix := t0.Add(-time.Second)
	mk := func(vehicle string, n int) []samples.Sample {
		var out []samples.Sample
		for i := 0; i < n; i++ {
			out = append(out, samples.Sample{
				VehicleID: vehicle, At: t0.Add(time.Duration(i) * 5 * time.Second),
				BatteryLevelPct: 70, RangeMi: 250, OdometerMi: 1000 + float64(i)*0.01,
				Lat: 30.27, Lon: -97.74, SpeedMph: 40, ShiftState: "D", DriveMode: "everyday",
				ChargingState: "charger_disconnected", Source: "live", DriveNumber: 7,
				LocationFixAt: &fix, AltitudeM: &alt, TirePressureMinBar: &tire,
				PackTempAvgC: &temp, PackTempMaxC: &temp, PackTempMinC: &temp, PowerState: "go",
				ChargerStatus: "chrgr_sts_not_connected",
			})
		}
		return out
	}
	tok := fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epoch}

	// 1,100 samples: three chunks. The owner writes them all; the same rows through the plain path on another
	// vehicle read back identically.
	if err := store.InsertBulk(fence.With(ctx, tok), mk("rv-guarded", 1100)); err != nil {
		t.Fatalf("guarded bulk with a valid lease: %v", err)
	}
	if err := store.InsertBulk(ctx, mk("rv-plain", 1100)); err != nil {
		t.Fatal(err)
	}
	var diff int
	if err := pool.QueryRowContext(ctx, `
		SELECT count(*) FROM (
		  SELECT at, battery_level_pct, range_mi, odometer_mi, lat, lon, location_fix_at, speed_mph, shift_state, drive_mode,
		         charging_state, drive_number, source, altitude_m, tire_pressure_min_bar, pack_temp_avg_c, pack_temp_max_c,
		         pack_temp_min_c, power_state, charger_status
		    FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id WHERE v.rivian_vehicle_id = 'rv-guarded'
		  EXCEPT
		  SELECT at, battery_level_pct, range_mi, odometer_mi, lat, lon, location_fix_at, speed_mph, shift_state, drive_mode,
		         charging_state, drive_number, source, altitude_m, tire_pressure_min_bar, pack_temp_avg_c, pack_temp_max_c,
		         pack_temp_min_c, power_state, charger_status
		    FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id WHERE v.rivian_vehicle_id = 'rv-plain') d`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id WHERE v.rivian_vehicle_id = 'rv-guarded'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1100 || diff != 0 {
		t.Fatalf("guarded rows = %d, differing from the plain path = %d, want 1100 and 0", n, diff)
	}
	// A repeat is idempotent, not a refusal.
	if err := store.InsertBulk(fence.With(ctx, tok), mk("rv-guarded", 100)); err != nil {
		t.Fatalf("repeat under a valid lease: %v", err)
	}

	// A stale epoch, another pod and no lease are refused and write nothing.
	for name, bad := range map[string]fence.Token{
		"stale epoch": {Group: tok.Group, Pod: "pod-a", Epoch: epoch - 1},
		"other pod":   {Group: tok.Group, Pod: "pod-b", Epoch: epoch},
	} {
		if err := store.InsertBulk(fence.With(ctx, bad), mk("rv-refused", 50)); !errors.Is(err, fence.ErrFenced) {
			t.Errorf("%s: err = %v, want ErrFenced", name, err)
		}
	}
	var refused int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id WHERE v.rivian_vehicle_id = 'rv-refused'`).Scan(&refused); err != nil {
		t.Fatal(err)
	}
	if refused != 0 {
		t.Errorf("a refused bulk wrote %d rows", refused)
	}
}
