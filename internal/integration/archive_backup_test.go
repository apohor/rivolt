//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestBackupFromArchive: the backup's samples for a month thinning reduced
// come from the archive in full, plus rows that reached the month later; an
// unthinned month comes from the database; a thinned month with a vehicle
// that has no object falls back to the reduced rows and is reported; a
// corrupt object fails the backup.
func TestBackupFromArchive(t *testing.T) {
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

	uid, err := db.EnsureUserFull(ctx, pool, "backup-archive", "", "Backup")
	if err != nil {
		t.Fatal(err)
	}
	var v1, v2 uuid.UUID
	for i, v := range []*uuid.UUID{&v1, &v2} {
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, []string{"RV-A", "RV-B"}[i]).Scan(v); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	thin := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -5, 0)
	recent := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	for _, m := range []time.Time{thin, recent} {
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, m); err != nil {
			t.Fatal(err)
		}
	}
	// One hour of driving at one row a second: thinning keeps about one in 30.
	day := thin.Add(48 * time.Hour)
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO vehicle_state (user_id, vehicle_id, at, source, shift_state, speed_mph, lat, drive_number)
		SELECT $1, $2, $3::timestamptz + make_interval(secs => g), 'test', 'D', 40, 30 + g / 1e5, 1
		  FROM generate_series(0, 3599) g`, uid, v1, day); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`, uid, v1, recent.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	arch, err := archive.NewArchiver(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := arch.Run(ctx); err != nil || n != 1 {
		t.Fatalf("archive: %d, %v", n, err)
	}
	if n, err := samples.NewThinner(pool, 90).Run(ctx); err != nil || n != 1 {
		t.Fatalf("thin: %d, %v", n, err)
	}
	var kept int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_state WHERE vehicle_id = $1 AND at < $2`, v1, thin.AddDate(0, 1, 0)).Scan(&kept); err != nil || kept >= 3600 || kept == 0 {
		t.Fatalf("thinned month keeps %d rows (%v)", kept, err)
	}

	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	reader, err := archive.NewReader(pool, cfg, sealer)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := 0
	reader.OnCorrupt = func(uuid.UUID, uuid.UUID, time.Time, error) { corrupt++ }
	collect := func() (map[string]int, []string, error) {
		got := map[string]int{}
		reduced, err := store.EachAllArchived(ctx, reader, func(v samples.Sample) error {
			got[v.VehicleID+" "+v.At.Format("2006-01")]++
			return nil
		})
		return got, reduced, err
	}
	key := func(v, m string) string { return v + " " + m }
	thinM, recentM := archive.MonthString(thin), archive.MonthString(recent)

	got, reduced, err := collect()
	if err != nil {
		t.Fatal(err)
	}
	if got[key("RV-A", thinM)] != 3600 || got[key("RV-A", recentM)] != 1 || len(reduced) != 0 {
		t.Fatalf("backup: %v, reduced %v; want 3600 archived rows for the thinned month, 1 recent", got, reduced)
	}

	// A row that reached the thinned month later, and a second vehicle with no
	// object in it: the first is added, the second is the reduced copy and the
	// month is reported.
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'late'), ($1, $4, $5, 'late')`,
		uid, v1, day.Add(-time.Hour), v2, day); err != nil {
		t.Fatal(err)
	}
	got, reduced, err = collect()
	if err != nil {
		t.Fatal(err)
	}
	if got[key("RV-A", thinM)] != 3601 || got[key("RV-B", thinM)] != 1 || len(reduced) != 1 || reduced[0] != thinM {
		t.Fatalf("with late rows: %v, reduced %v", got, reduced)
	}

	// The object marked for re-archiving (as a writer does when rows land in an
	// archived month) is still used: object plus the rows it lacks (DA on A4).
	if _, err := pool.ExecContext(ctx, `UPDATE archive_months SET needs_rearchive = true WHERE vehicle_id = $1`, v1); err != nil {
		t.Fatal(err)
	}
	got, reduced, err = collect()
	if err != nil {
		t.Fatal(err)
	}
	if got[key("RV-A", thinM)] != 3601 || len(reduced) != 1 {
		t.Fatalf("with needs_rearchive: %v, reduced %v; want the object still used", got, reduced)
	}

	// The object corrupted in the store: the backup fails rather than ending
	// short, and the corruption is reported (full detail lost: an alert).
	obj := archive.ObjectKey("test", uid, v1, thin)
	body, err := bucket.Get(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(body)
	body.Close()
	bad := buf.Bytes()
	bad[len(bad)-5] ^= 1
	if err := bucket.Put(ctx, obj, bad, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collect(); err == nil {
		t.Error("a corrupt archive object didn't fail the backup")
	}
	if corrupt != 1 {
		t.Errorf("OnCorrupt called %d times, want 1", corrupt)
	}

	// The archive off: the database alone, as before.
	n := 0
	if _, err := store.EachAllArchived(ctx, nil, func(samples.Sample) error { n++; return nil }); err != nil || n != kept+1+2 {
		t.Errorf("without the archive: %d rows (%v), want %d", n, err, kept+3)
	}
}
