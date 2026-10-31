//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/histfloor"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
)

// The history floor: after a user deletes history, what writes by itself must not bring it back. A receipt older than the floor is not
// re-inserted by the sync, a drive or charge that STARTED before the floor is dropped whole (a late close of one that crossed it too),
// replay-catch-up samples are cut at the floor, an archived month wholly before it is not restored, the floor never goes down, and a
// user's own import or restore (Bypass) is not blocked. Another account is unaffected.
func TestHistoryFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "floor-user")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "floor-other")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'RV-FLOOR') RETURNING id`, uid).Scan(&vid); err != nil {
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

	now := time.Now().UTC().Truncate(time.Second)
	floor := now.AddDate(0, 0, -30)
	before, after := floor.AddDate(0, 0, -10), floor.AddDate(0, 0, 10)
	if err := histfloor.Raise(ctx, pool, uid, floor); err != nil {
		t.Fatal(err)
	}
	// It only rises.
	if err := histfloor.Raise(ctx, pool, uid, floor.AddDate(0, 0, -90)); err != nil {
		t.Fatal(err)
	}
	if got, _ := histfloor.Get(ctx, pool, uid); !got.Equal(floor) {
		t.Fatalf("floor after a lower Raise = %v, want %v", got, floor)
	}

	// Receipts: the sync brings back Rivian's whole history; only receipts from the floor on are stored.
	rs := receipts.NewStore(pool)
	sums := []rivian.SessionSummary{
		{TransactionID: "fl-old", StartedAt: before, EndedAt: before.Add(time.Hour), City: "Testville"},
		{TransactionID: "fl-new", StartedAt: after, EndedAt: after.Add(time.Hour)},
		{TransactionID: "fl-cross", StartedAt: floor.Add(-time.Hour), EndedAt: floor.Add(time.Hour)}, // crosses the floor: dropped whole
	}
	if err := rs.UpsertReceipts(ctx, uid, sums); err != nil {
		t.Fatal(err)
	}
	if err := rs.UpsertReceipts(ctx, other, sums); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM rivian_receipts WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("receipts after the floor: %d, want only the one after it", n)
	}
	if n := count(`SELECT count(*) FROM rivian_receipts WHERE user_id = $1`, other); n != 3 {
		t.Errorf("another account's receipts: %d, want 3 (no floor)", n)
	}

	// Drives and charges: dropped whole when they start before the floor, including the late close of one that crossed it.
	resolvers := db.NewVehicleResolverFactory(pool)
	ds, cs, ss := drives.NewFactory(pool, resolvers).For(uid), charges.NewFactory(pool, resolvers).For(uid), samples.NewFactory(pool, resolvers).For(uid)
	for id, at := range map[string]time.Time{"fl-drive-old": before, "fl-drive-cross": floor.Add(-time.Minute), "fl-drive-new": after} {
		if err := ds.Upsert(ctx, drives.Drive{ID: id, VehicleID: "RV-FLOOR", StartedAt: at, EndedAt: at.Add(time.Hour), DistanceMi: 3, Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	for id, at := range map[string]time.Time{"fl-charge-old": before, "fl-charge-cross": floor.Add(-time.Minute), "fl-charge-new": after} {
		if err := cs.Upsert(ctx, charges.Charge{ID: id, VehicleID: "RV-FLOOR", StartedAt: at, EndedAt: at.Add(time.Hour), EnergyAddedKWh: 5,
			MaxPowerKW: 11, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(`SELECT count(*) FROM drives WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("drives: %d, want only the one that started after the floor", n)
	}
	if n := count(`SELECT count(*) FROM charges WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("charges: %d, want only the one that started after the floor", n)
	}

	// Replay catch-up: samples are cut at the floor, per row.
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, after); err != nil {
		t.Fatal(err)
	}
	if err := ss.InsertBulk(ctx, []samples.Sample{
		{VehicleID: "RV-FLOOR", At: before, BatteryLevelPct: 50},
		{VehicleID: "RV-FLOOR", At: after, BatteryLevelPct: 51},
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM vehicle_state WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("samples: %d, want only the one after the floor", n)
	}

	// The user's own import or restore is not blocked.
	if err := ds.Upsert(histfloor.Bypass(ctx), drives.Drive{ID: "fl-drive-restored", VehicleID: "RV-FLOOR", StartedAt: before, EndedAt: before.Add(time.Hour), DistanceMi: 3, Source: "import"}); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM drives WHERE user_id = $1 AND external_id = 'fl-drive-restored'`, uid); n != 1 {
		t.Errorf("a user's own restore was blocked by the floor: %d", n)
	}

	// An archived month wholly before the floor is not restored.
	sealer, err := crypto.NewEnvSealer("test", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	rst, err := archive.NewRestorer(pool, archive.Config{Env: "test", Endpoint: "http://127.0.0.1:1", Bucket: "none", Region: "us-east-1",
		AccessKey: "x", SecretKey: "x"}, sealer)
	if err != nil {
		t.Fatal(err)
	}
	oldMonth := time.Date(before.Year(), before.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -2, 0)
	if _, err := pool.ExecContext(ctx, `INSERT INTO archive_months (user_id, vehicle_id, month, rows, first_at, last_at, sha256_object, bytes, verified_at)
		VALUES ($1, $2, $3::date, 1, $3::timestamptz, $3::timestamptz, 'x', 1, now())`, uid, vid, oldMonth); err != nil {
		t.Fatal(err)
	}
	if _, err := rst.Restore(ctx, uid, vid, oldMonth, false, false); !errors.Is(err, archive.ErrNoObject) {
		t.Errorf("restore of a month before the floor: %v, want ErrNoObject", err)
	}
}
