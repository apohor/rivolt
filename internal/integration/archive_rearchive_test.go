//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestArchiveRearchive (N19 A4a): rows written into an archived month through
// the samples store flag it; the archive job rewrites the object from the
// database when the month wasn't thinned, and merges with the object when it
// was (every original row kept, the object's row winning where both have
// one); a flag set during a run survives it; a write that inserts nothing
// flags nothing.
func TestArchiveRearchive(t *testing.T) {
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
	reader, err := archive.NewReader(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}

	uid, err := db.EnsureUserFull(ctx, pool, "rearchive", "", "Rearchive")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'RV-R') RETURNING id`, uid).Scan(&vid); err != nil {
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
	// The thinned month: an hour of driving at one row a second; the
	// source of row 0 has a comma and a quote, to survive the round trip.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO vehicle_state (user_id, vehicle_id, at, source, shift_state, speed_mph, lat, drive_number)
		SELECT $1, $2, $3::timestamptz + make_interval(secs => g), CASE WHEN g = 0 THEN 'odd, "src"' ELSE 'test' END, 'D', 40, 30 + g / 1e5, 1
		  FROM generate_series(0, 3599) g`, uid, vid, day); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`,
			uid, vid, plain.Add(time.Duration(30+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ { // oldest month first, one per run
		if n, err := arch.Run(ctx); err != nil || n != 1 {
			t.Fatalf("archive run %d: %d, %v", i, n, err)
		}
	}
	if n, err := samples.NewThinner(pool, 90).Run(ctx); err != nil || n != 1 {
		t.Fatalf("thin: %d, %v", n, err)
	}

	type state struct {
		rows      int64
		needs     bool
		requested bool
	}
	monthState := func(m time.Time) state {
		t.Helper()
		var s state
		var req *time.Time
		if err := pool.QueryRowContext(ctx, `SELECT rows, needs_rearchive, rearchive_requested_at FROM archive_months
			WHERE user_id = $1 AND vehicle_id = $2 AND month = $3::date`, uid, vid, m).Scan(&s.rows, &s.needs, &req); err != nil {
			t.Fatal(err)
		}
		s.requested = req != nil
		return s
	}
	objectRows := func(m time.Time) (n int, sources map[string]bool) {
		t.Helper()
		sources = map[string]bool{}
		if err := reader.EachRow(ctx, uid, vid, m, func(r archive.Row) error {
			n++
			s, _ := r.Get("source")
			sources[s] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return n, sources
	}

	// Rows land in both archived months through the store (as restore and
	// the importer write): both are flagged, in the insert's transaction.
	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	lateThin, latePlain := day.Add(-time.Hour), plain.Add(40*time.Hour)
	if err := store.InsertBatch(ctx, []samples.Sample{
		{VehicleID: "RV-R", At: lateThin, Source: "late"},
		{VehicleID: "RV-R", At: latePlain, Source: "late"},
	}); err != nil {
		t.Fatal(err)
	}
	if s := monthState(thin); !s.needs || !s.requested {
		t.Fatalf("thinned month not flagged: %+v", s)
	}
	if s := monthState(plain); !s.needs || !s.requested {
		t.Fatalf("plain month not flagged: %+v", s)
	}
	for i := 0; i < 2; i++ {
		if n, err := arch.Run(ctx); err != nil || n != 1 {
			t.Fatalf("re-archive run %d: %d, %v", i, n, err)
		}
	}
	// The thinned month: merged, so every original row plus the late one.
	if s := monthState(thin); s.needs || s.rows != 3601 {
		t.Errorf("thinned month after the merge: %+v, want 3601 rows, not flagged", s)
	}
	if n, src := objectRows(thin); n != 3601 || !src["late"] || !src[`odd, "src"`] {
		t.Errorf("merged object: %d rows, sources %v", n, src)
	}
	// The plain month: rewritten from the database.
	if s := monthState(plain); s.needs || s.rows != 4 {
		t.Errorf("plain month after the re-archive: %+v, want 4 rows, not flagged", s)
	}
	if n, _ := objectRows(plain); n != 4 {
		t.Errorf("plain object: %d rows, want 4", n)
	}
	// No .new copies left behind.
	if err := bucket.List(ctx, archive.UserPrefix("test", uid), func(o s3.Object) error {
		if strings.HasSuffix(o.Key, ".new") {
			t.Errorf("left behind: %s", o.Key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A flag set after a run started (simulated: requested in the future)
	// survives the run, so that write is archived next time.
	if _, err := pool.ExecContext(ctx, `UPDATE archive_months SET needs_rearchive = TRUE, rearchive_requested_at = now() + interval '1 hour'
		WHERE user_id = $1 AND month = $2::date`, uid, plain); err != nil {
		t.Fatal(err)
	}
	if n, err := arch.Run(ctx); err != nil || n != 1 {
		t.Fatalf("run with a later flag: %d, %v", n, err)
	}
	if s := monthState(plain); !s.needs {
		t.Error("a flag set during the run was cleared")
	}

	// A write that inserts nothing (the row exists) flags nothing.
	if _, err := pool.ExecContext(ctx, `UPDATE archive_months SET needs_rearchive = FALSE, rearchive_requested_at = NULL WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertBatch(ctx, []samples.Sample{{VehicleID: "RV-R", At: latePlain, Source: "late"}}); err != nil {
		t.Fatal(err)
	}
	if s := monthState(plain); s.needs || s.requested {
		t.Errorf("a duplicate write flagged the month: %+v", s)
	}
}
