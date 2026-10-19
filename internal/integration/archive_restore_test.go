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
	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestArchiveRestore (N19 A4c): an archived, thinned vehicle-month is put
// back in full detail (a dry run first changes nothing), held from thinning
// for a week, then thinned again for that vehicle only; a restore is refused
// while maintenance runs and for a month without an object; and a row that
// reached an archived month without a flag (a write during its first
// archive) gets the month flagged when thinning refuses it (DA on #628).
func TestArchiveRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	endpoint := startMinIO(ctx, t)
	bucket := &s3.Client{Endpoint: endpoint, Region: "us-east-1", Bucket: "rivolt-telemetry-archive",
		AccessKey: "rivolt-test", SecretKey: "rivolt-test-secret"}
	for i := 0; ; i++ {
		err := bucket.CreateBucket(ctx)
		if err == nil {
			break
		}
		if i == 30 {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	sealer, err := crypto.NewEnvSealer("test", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := archive.Config{Env: "test", Endpoint: endpoint, Bucket: bucket.Bucket, Region: "us-east-1",
		AccessKey: "rivolt-test", SecretKey: "rivolt-test-secret"}
	arch, err := archive.NewArchiver(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := archive.NewRestorer(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := db.EnsureUserFull(ctx, pool, "restore", "", "Restore")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'RV-S') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	thin := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -6, 0)
	plain := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -5, 0)
	for _, m := range []time.Time{thin, plain} {
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, m); err != nil {
			t.Fatal(err)
		}
	}
	day := thin.Add(48 * time.Hour)
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO vehicle_state (user_id, vehicle_id, at, source, shift_state, speed_mph, lat, drive_number)
		SELECT $1, $2, $3::timestamptz + make_interval(secs => g), 'test', 'D', 40, 30 + g / 1e5, 1
		  FROM generate_series(0, 3599) g`, uid, vid, day); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`,
			uid, vid, plain.Add(time.Duration(30+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if n, err := arch.Run(ctx); err != nil || n != 1 {
			t.Fatalf("archive run %d: %d, %v", i, n, err)
		}
	}
	var rethinned []int64
	repaired := int64(0)
	// th thins only the older month (its age threshold sits between the two
	// months' ends); th90 is the production threshold, for the repair step.
	plainEnded := int(time.Since(plain.AddDate(0, 1, 0)).Hours() / 24)
	th := samples.NewThinner(pool, plainEnded+1)
	th90 := samples.NewThinner(pool, 90)
	for _, x := range []*samples.Thinner{th, th90} {
		x.OnRethin = func(_ string, _, _ uuid.UUID, deleted int64) { rethinned = append(rethinned, deleted) }
		x.OnRepair = func(_ string, n int64) { repaired += n }
	}
	if n, err := th.Run(ctx); err != nil || n != 1 {
		t.Fatalf("thin: %d, %v", n, err)
	}
	count := func(m time.Time) int64 {
		t.Helper()
		var n int64
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_state WHERE vehicle_id = $1 AND at >= $2 AND at < $3`,
			vid, m, m.AddDate(0, 1, 0)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	kept := count(thin)
	if kept >= 3600 || kept == 0 {
		t.Fatalf("thinned month keeps %d", kept)
	}

	// Refusals: no object for that month; maintenance running.
	if _, err := rs.Restore(ctx, uid, vid, thin.AddDate(0, -1, 0), true, false); !errors.Is(err, archive.ErrNoObject) {
		t.Errorf("restore without an object: %v", err)
	}
	holder, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(0x72765f72657465)); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Restore(ctx, uid, vid, thin, true, false); !errors.Is(err, archive.ErrBusy) {
		t.Errorf("restore during maintenance: %v, want ErrBusy", err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, int64(0x72765f72657465)); err != nil {
		t.Fatal(err)
	}
	holder.Close()

	// A dry run says what it would do and changes nothing.
	res, err := rs.Restore(ctx, uid, vid, thin, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.InObject != 3600 || res.Present != kept || res.Inserted != 3600-kept || res.DBOnly != 0 || count(thin) != kept {
		t.Fatalf("dry run %+v, %d rows in the month", res, count(thin))
	}
	// The restore puts the month back and holds it for a week.
	res, err = rs.Restore(ctx, uid, vid, thin, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 3600-kept || count(thin) != 3600 || res.RethinAfter == nil || res.RethinAfter.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("restore %+v, %d rows", res, count(thin))
	}
	var needs bool
	if err := pool.QueryRowContext(ctx, `SELECT needs_rearchive FROM archive_months WHERE vehicle_id = $1 AND month = $2::date`, vid, thin).Scan(&needs); err != nil || needs {
		t.Errorf("a restore with no extra rows flagged the month (%v, %v)", needs, err)
	}
	// Held: thinning leaves it.
	if _, err := th.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if count(thin) != 3600 || len(rethinned) != 0 {
		t.Fatalf("thinned while held: %d rows", count(thin))
	}
	// Hold over: that vehicle is thinned again, back to what it kept.
	if _, err := pool.ExecContext(ctx, `UPDATE archive_months SET rethin_after = now() - interval '1 minute' WHERE vehicle_id = $1 AND month = $2::date`, vid, thin); err != nil {
		t.Fatal(err)
	}
	if _, err := th.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if count(thin) != kept || len(rethinned) != 1 || rethinned[0] != 3600-kept {
		t.Errorf("after the hold: %d rows (want %d), rethins %v", count(thin), kept, rethinned)
	}
	var held *time.Time
	if err := pool.QueryRowContext(ctx, `SELECT rethin_after FROM archive_months WHERE vehicle_id = $1 AND month = $2::date`, vid, thin).Scan(&held); err != nil || held != nil {
		t.Errorf("rethin_after not cleared: %v, %v", held, err)
	}

	// A row in the archived, unthinned month without a flag (as if written
	// during its first archive): thinning refuses the month and flags it.
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'race')`,
		uid, vid, plain.Add(60*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := th90.Run(ctx); !errors.Is(err, samples.ErrNotArchived) {
		t.Fatalf("thinning a month whose object lacks a row: %v, want ErrNotArchived", err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT needs_rearchive FROM archive_months WHERE vehicle_id = $1 AND month = $2::date`, vid, plain).Scan(&needs); err != nil || !needs || repaired != 1 {
		t.Errorf("mismatched month not flagged (%v, %v, repaired %d)", needs, err, repaired)
	}
	// The next archive run heals it, and thinning goes ahead.
	if n, err := arch.Run(ctx); err != nil || n != 1 {
		t.Fatalf("healing run: %d, %v", n, err)
	}
	if n, err := th90.Run(ctx); err != nil || n != 1 {
		t.Errorf("thinning after the heal: %d, %v", n, err)
	}
}
