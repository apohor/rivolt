//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestSampleThinning: one month of 1 Hz samples five months back (a drive, a
// charge, parked time). A dry run reports and changes nothing; a real run keeps
// every drive and charge end, every gear/charging change and one row per 30 s
// driving, 2 min charging and 15 min parked, keeps the full copy detached,
// records sample_tiers; a second run thins nothing; 8 days later the full copy
// is dropped.
func TestSampleThinning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserFull(ctx, pool, "thin-user", "", "Thin")
	if err != nil {
		t.Fatal(err)
	}
	var vid string
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'rv-thin') RETURNING id::text`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), 10, 8, 0, 0, 0, time.UTC).AddDate(0, -5, 0)
	month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, day); err != nil {
		t.Fatal(err)
	}
	// 20 min driving (drive 1), 10 min parked, 30 min charging (charge 1), 2 h parked: 1 row a second.
	ins := `INSERT INTO vehicle_state (user_id, vehicle_id, at, source, shift_state, speed_mph, charging_state, charger_power_kw, drive_number, charge_number)
		SELECT $1, $2, $3::timestamptz + make_interval(secs => g), 'test',
		       CASE WHEN g < 1200 THEN 'D' ELSE 'P' END,
		       CASE WHEN g < 1200 THEN 40 ELSE 0 END,
		       CASE WHEN g >= 1800 AND g < 3600 THEN 'charging_active' ELSE 'charging_ready' END,
		       CASE WHEN g >= 1800 AND g < 3600 THEN 11 ELSE 0 END,
		       CASE WHEN g < 1200 THEN 1 END,
		       CASE WHEN g >= 1800 AND g < 3600 THEN 1 END
		  FROM generate_series(0, 10799) g`
	if _, err := pool.ExecContext(ctx, ins, uid, vid, day); err != nil {
		t.Fatal(err)
	}
	count := func() (n int64) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_state WHERE vehicle_id = $1`, vid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 10800 {
		t.Fatalf("seeded %d", n)
	}

	var reports []samples.ThinReport
	th := samples.NewThinner(pool, 90)
	th.Report = func(r samples.ThinReport) { reports = append(reports, r) }
	th.DryRun = true
	if n, err := th.Run(ctx); err != nil || n != 0 || len(reports) != 1 || !reports[0].DryRun {
		t.Fatalf("dry run: %d %v %+v", n, err, reports)
	}
	if count() != 10800 {
		t.Fatal("dry run changed rows")
	}
	would := reports[0].RowsAfter

	th.DryRun = false
	if n, err := th.Run(ctx); err != nil || n != 1 {
		t.Fatalf("run: %d %v", n, err)
	}
	after := count()
	// driving 1200 s / 30 = 40, charging 1800 s / 120 = 15, parked 7800 s / 900 ≈ 10 buckets (split by the
	// charging/parked boundaries), plus ends and changes: a few dozen more at most.
	if after != would || after < 60 || after > 120 {
		t.Errorf("kept %d rows (dry run said %d), want about 70", after, would)
	}
	for _, at := range []time.Time{day, day.Add(1199 * time.Second), day.Add(1800 * time.Second), day.Add(3599 * time.Second), day.Add(1200 * time.Second)} {
		var ok bool
		if err := pool.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM vehicle_state WHERE vehicle_id = $1 AND at = $2)`, vid, at).Scan(&ok); err != nil || !ok {
			t.Errorf("row at +%v (an end or a gear change) not kept", at.Sub(day))
		}
	}
	var rowsBefore, rowsAfter int64
	if err := pool.QueryRowContext(ctx, `SELECT rows_before, rows_after FROM sample_tiers WHERE month = $1`, month).Scan(&rowsBefore, &rowsAfter); err != nil || rowsBefore != 10800 || rowsAfter != after {
		t.Errorf("sample_tiers %d→%d, %v", rowsBefore, rowsAfter, err)
	}
	full := "vehicle_state_y" + month.Format("2006_01") + "_full"
	var fullRows int64
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM "`+full+`"`).Scan(&fullRows); err != nil || fullRows != 10800 {
		t.Errorf("full copy %s: %d rows, %v", full, fullRows, err)
	}
	// The live month still takes writes.
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`, uid, vid, now); err != nil {
		t.Fatalf("insert into the current month: %v", err)
	}
	if n, err := th.Run(ctx); err != nil || n != 0 {
		t.Errorf("second run thinned %d (%v)", n, err)
	}
	th.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if _, err := th.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := pool.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, full).Scan(&exists); err != nil || exists {
		t.Errorf("full copy still there after 8 days (%v)", err)
	}
}
