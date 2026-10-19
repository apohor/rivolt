//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestSampleRetentionDropsOldPartitions: with 12 months of retention the sweep
// drops the monthly partitions wholly before the cutoff (their rows go with
// them), keeps the cutoff month, the current month and the future ones, and a
// second sweep changes nothing.
func TestSampleRetentionDropsOldPartitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserFull(ctx, pool, "retention-user", "", "Retention")
	if err != nil {
		t.Fatal(err)
	}
	var vid string
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'rv-ret') RETURNING id::text`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	month := func(back int) time.Time {
		return time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 0, time.UTC).AddDate(0, -back, 0)
	}
	// A row in every month from 14 months back to now.
	for back := 14; back >= 0; back-- {
		at := month(back)
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`, uid, vid, at); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (rows int, oldest time.Time) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT count(*), min(at) FROM vehicle_state WHERE vehicle_id = $1`, vid).Scan(&rows, &oldest); err != nil {
			t.Fatal(err)
		}
		return rows, oldest.UTC()
	}
	if n, _ := count(); n != 15 {
		t.Fatalf("seeded %d rows", n)
	}

	r := samples.NewRetention(pool, 12)
	dropped, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 14 and 13 months back are wholly before the cutoff; 12 months back is the
	// cutoff month itself and stays.
	if dropped != 2 {
		t.Errorf("dropped %d partitions, want 2", dropped)
	}
	n, oldest := count()
	if n != 13 || !oldest.Equal(month(12)) {
		t.Errorf("after the sweep: %d rows, oldest %s; want 13, oldest %s", n, oldest, month(12))
	}
	cutoff := samples.RetentionCutoff(now, 12)
	var gone int
	if err := pool.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_class
		 WHERE relname = 'vehicle_state_y' || to_char($1::timestamptz AT TIME ZONE 'UTC', 'YYYY_MM')`, cutoff.AddDate(0, -1, 0)).Scan(&gone); err != nil || gone != 0 {
		t.Errorf("the partition before the cutoff still exists (%d, %v)", gone, err)
	}
	// The live month still takes writes.
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`, uid, vid, now); err != nil {
		t.Fatalf("insert into the current month after the sweep: %v", err)
	}
	if again, err := r.Run(ctx); err != nil || again != 0 {
		t.Errorf("second sweep: %d, %v; want nothing", again, err)
	}
	if off := samples.NewRetention(pool, 0); off != nil {
		t.Error("0 months must be no sweep")
	}
}
