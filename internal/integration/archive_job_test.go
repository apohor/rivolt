//go:build integration

package integration_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
)

// TestArchiveJob: the archive job writes one encrypted object per (user,
// vehicle) for the oldest month past ArchiveAfterDays, reads it back and
// records it verified; it leaves recent months and needs_rearchive rows
// alone, records nothing for an object that failed, and a second run is a
// no-op. The object decrypts with the user's key to the exact rows,
// NULLs and quoting included.
func TestArchiveJob(t *testing.T) {
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
	if _, err := archive.NewArchiver(pool, cfg, crypto.NoopSealer{}); err == nil {
		t.Fatal("archiver accepted the no-op sealer")
	}

	uid, err := db.EnsureUserFull(ctx, pool, "archive-user", "", "Archive")
	if err != nil {
		t.Fatal(err)
	}
	var v1, v2 uuid.UUID
	for i, v := range []*uuid.UUID{&v1, &v2} {
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, "rv-arch-"+string(rune('a'+i))).Scan(v); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	older := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -6, 0)
	old := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -5, 0)
	recent := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	for _, m := range []time.Time{older, old, recent} {
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, m); err != nil {
			t.Fatal(err)
		}
	}
	ins := func(vid uuid.UUID, at time.Time, lat any, source string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, lat, source) VALUES ($1, $2, $3, $4, $5)`, uid, vid, at, lat, source); err != nil {
			t.Fatal(err)
		}
	}
	t0 := old.Add(36 * time.Hour)
	ins(v1, t0, 30.123456789, "rivian")
	ins(v1, t0.Add(1500*time.Microsecond), nil, `odd,"source"`+"\nline")
	ins(v1, t0.Add(time.Hour), -97.5, "")
	ins(v2, t0, 1.0, "rivian")
	ins(v1, recent.Add(time.Hour), 1.0, "rivian")
	// The older month is big (3000 rows of noisy positions), so a small size
	// cap fails it while the old month's 3 rows fit.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO vehicle_state (user_id, vehicle_id, at, lat, lon, source)
		SELECT $1, $2, $3::timestamptz + make_interval(secs => g * 7), random() * 90, random() * 180, 'rivian'
		  FROM generate_series(0, 2999) g`, uid, v1, older.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// v2's month is marked for re-archiving (A4's merge): the job leaves it.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO archive_months (user_id, vehicle_id, month, rows, first_at, last_at, sha256_object, bytes, needs_rearchive)
		VALUES ($1, $2, $3::date, 1, $4, $4, 'x', 1, true)`, uid, v2, old, t0); err != nil {
		t.Fatal(err)
	}
	verified := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM archive_months WHERE verified_at IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// With the store unreachable the run stops at the first object instead of
	// building every pending object of every month to fail each upload.
	down := cfg
	down.Endpoint = "http://127.0.0.1:1"
	unreachable, err := archive.NewArchiver(pool, down, sealer)
	if err != nil {
		t.Fatal(err)
	}
	var downReports []archive.Report
	unreachable.Report = func(r archive.Report) {
		if !r.Done {
			downReports = append(downReports, r)
		}
	}
	if n, err := unreachable.Run(ctx); err == nil || n != 0 || len(downReports) != 1 {
		t.Fatalf("store down: %d, %v, %d objects tried; want an error after 1", n, err, len(downReports))
	}

	// An object over the size cap fails and is not recorded; the month it is
	// in doesn't hold the next one back (DA): the old month is archived in the
	// same run, and the run still reports the failure.
	small, err := archive.NewArchiver(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}
	small.MaxObjectBytes = 4096
	if n, err := small.Run(ctx); err == nil || n != 1 {
		t.Fatalf("capped run: %d, %v; want 1 object and an error", n, err)
	}
	var olderVerified int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM archive_months WHERE month = $1::date`, older).Scan(&olderVerified); err != nil || olderVerified != 0 {
		t.Fatalf("the failed object was recorded (%d, %v)", olderVerified, err)
	}
	if verified() != 1 {
		t.Fatalf("%d verified after the capped run, want the old month's 1", verified())
	}

	a, err := archive.NewArchiver(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}
	var reports []archive.Report
	a.Report = func(r archive.Report) { reports = append(reports, r) }
	// Uncapped, the next run archives the older month's object.
	n, err := a.Run(ctx)
	if err != nil || n != 1 {
		t.Fatalf("run: %d objects, %v; want 1", n, err)
	}
	if len(reports) != 2 || reports[0].Rows != 3000 || reports[0].Err != nil || !reports[1].Done || reports[1].Objects != 1 {
		t.Errorf("reports %+v", reports)
	}
	var rows, size int64
	var first, last time.Time
	if err := pool.QueryRowContext(ctx, `
		SELECT rows, bytes, first_at, last_at FROM archive_months
		 WHERE user_id = $1 AND vehicle_id = $2 AND month = $3::date AND verified_at IS NOT NULL`, uid, v1, old).Scan(&rows, &size, &first, &last); err != nil {
		t.Fatalf("v1's month not recorded verified: %v", err)
	}
	if rows != 3 || !first.Equal(t0) || !last.Equal(t0.Add(time.Hour)) || size <= 0 {
		t.Errorf("recorded rows=%d bytes=%d first=%s last=%s", rows, size, first, last)
	}
	if verified() != 2 {
		t.Errorf("%d verified rows, want 2 (older and old for v1); the recent month and v2 must not be archived", verified())
	}
	if n, err := a.Run(ctx); err != nil || n != 0 {
		t.Errorf("second run: %d, %v; want a no-op", n, err)
	}

	// The object opens with the user's key and holds the rows exactly.
	var sealed []byte
	if err := pool.QueryRowContext(ctx, `SELECT sealed_key FROM archive_keys WHERE user_id = $1`, uid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	key, err := archive.OpenDataKey(ctx, sealer, uid, sealed)
	if err != nil {
		t.Fatal(err)
	}
	body, err := bucket.Get(ctx, archive.ObjectKey("test", uid, v1, old))
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := io.ReadAll(body)
	body.Close()
	dec, err := archive.NewDecryptReader(bytes.NewReader(obj), key, archive.ObjectAAD(uid, v1, archive.MonthString(old)))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(gz).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("%d CSV records, want header + 3", len(recs))
	}
	col := func(name string) int {
		i := slices.Index(recs[0], name)
		if i < 0 {
			t.Fatalf("no %s column in %v", name, recs[0])
		}
		return i
	}
	at, lat, src := col("at"), col("lat"), col("source")
	if recs[1][at] != t0.Format(archive.TimeLayout) || recs[2][at] != t0.Add(1500*time.Microsecond).Format(archive.TimeLayout) {
		t.Errorf("at %q, %q", recs[1][at], recs[2][at])
	}
	if recs[1][lat] != "30.123456789" || recs[2][lat] != "" || recs[3][lat] != "-97.5" {
		t.Errorf("lat %q %q %q", recs[1][lat], recs[2][lat], recs[3][lat])
	}
	if recs[2][src] != `odd,"source"`+"\nline" {
		t.Errorf("source %q", recs[2][src])
	}
	// The manifest is there and carries no values.
	mb, err := bucket.Get(ctx, archive.ManifestKey("test", uid, v1, old))
	if err != nil {
		t.Fatal(err)
	}
	man, _ := io.ReadAll(mb)
	mb.Close()
	if !bytes.Contains(man, []byte(`"rows":3`)) || bytes.Contains(man, []byte("30.123")) || bytes.Contains(man, []byte("first_at")) {
		t.Errorf("manifest %s", man)
	}
}
