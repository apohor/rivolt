//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
)

// TestArchiveDeletion: account deletion and history reset shred the user's
// archive at once (key gone) and queue the objects; one deletion run removes
// them, keeps an archive written after the reset under the new key, refuses a
// request whose prefix isn't its user's, reports requests pending past 24 h,
// and purges finished requests after 30 days. Both paths also clear the
// user's rows from thinning's detached full copies.
func TestArchiveDeletion(t *testing.T) {
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
	del, err := archive.NewDeleter(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Both clocks are this host's; the sleeps below put whole seconds between
	// the old objects, the requests and the new archive.
	del.ClockMargin = 0

	now := time.Now().UTC()
	month := func(back int) time.Time {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -back, 0)
	}
	for _, b := range []int{5, 6} {
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, month(b)); err != nil {
			t.Fatal(err)
		}
	}
	// A stand-in for a thinned month's detached full copy.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE vehicle_state_y2020_01_full (LIKE vehicle_state)`); err != nil {
		t.Fatal(err)
	}
	type acct struct {
		uid uuid.UUID
		vid uuid.UUID
	}
	mk := func(name string) acct {
		t.Helper()
		uid, err := db.EnsureUserFull(ctx, pool, name, "", name)
		if err != nil {
			t.Fatal(err)
		}
		var vid uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, "rv-"+name).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`,
				uid, vid, month(5).Add(time.Duration(24+i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state_y2020_01_full (user_id, vehicle_id, at, source) VALUES ($1, $2, '2020-01-02', 'test')`, uid, vid); err != nil {
			t.Fatal(err)
		}
		return acct{uid, vid}
	}
	gone, reset, other := mk("arch-del-a"), mk("arch-del-b"), mk("arch-del-c")
	if n, err := arch.Run(ctx); err != nil || n != 3 {
		t.Fatalf("archive run: %d, %v; want 3 objects", n, err)
	}
	objects := func(uid uuid.UUID) []s3.Object {
		t.Helper()
		var out []s3.Object
		if err := bucket.List(ctx, archive.UserPrefix("test", uid), func(o s3.Object) error { out = append(out, o); return nil }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if len(objects(gone.uid)) != 2 || len(objects(reset.uid)) != 2 {
		t.Fatalf("want an object and a manifest per user before deletion")
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	time.Sleep(1100 * time.Millisecond)
	// Account deletion: key and records go with the user, the objects are queued.
	if err := db.DeleteUserAccount(ctx, pool, gone.uid); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM archive_keys WHERE user_id = $1`, gone.uid) != 0 ||
		count(`SELECT count(*) FROM archive_months WHERE user_id = $1`, gone.uid) != 0 {
		t.Error("account deletion left the archive key or records")
	}
	if count(`SELECT count(*) FROM archive_deletions WHERE user_id = $1 AND prefix = $2 AND done_at IS NULL`, gone.uid, "v1/u="+gone.uid.String()+"/") != 1 {
		t.Error("account deletion queued no archive deletion")
	}

	// History reset: same, and the user keeps their account.
	stale, err := bucket.Get(ctx, archive.ObjectKey("test", reset.uid, reset.vid, month(5)))
	if err != nil {
		t.Fatal(err)
	}
	staleObj, _ := io.ReadAll(stale)
	stale.Close()
	if err := db.ResetUserArchive(ctx, pool, reset.uid); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM archive_keys WHERE user_id = $1`, reset.uid) != 0 ||
		count(`SELECT count(*) FROM archive_months WHERE user_id = $1`, reset.uid) != 0 ||
		count(`SELECT count(*) FROM archive_deletions WHERE user_id = $1 AND done_at IS NULL`, reset.uid) != 1 {
		t.Error("history reset left the key or records, or queued nothing")
	}
	for _, u := range []acct{gone, reset} {
		if count(`SELECT count(*) FROM vehicle_state_y2020_01_full WHERE user_id = $1`, u.uid) != 0 {
			t.Errorf("user %s's rows are still in a thinned full copy", u.uid)
		}
	}
	if count(`SELECT count(*) FROM vehicle_state_y2020_01_full WHERE user_id = $1`, other.uid) != 1 {
		t.Error("another user's full-copy rows were deleted")
	}
	// After the reset the user records again and an old month is archived
	// under a new key, after the request.
	time.Sleep(1100 * time.Millisecond)
	if _, err := pool.ExecContext(ctx, `DELETE FROM vehicle_state WHERE user_id = $1`, reset.uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`, reset.uid, reset.vid, month(6).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := arch.Run(ctx); err != nil || n != 1 {
		t.Fatalf("archive after reset: %d, %v", n, err)
	}

	// A request whose prefix isn't its user's fails and stays pending; it is
	// also overdue. A request finished 31 days ago is purged.
	stray := uuid.New()
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO archive_deletions (user_id, prefix, requested_at) VALUES ($1, $2, NOW() - interval '25 hours');
		`, stray, "v1/u="+other.uid.String()+"/"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO archive_deletions (user_id, prefix, requested_at, done_at) VALUES ($1, 'v1/u=x/', NOW() - interval '40 days', NOW() - interval '31 days')`, uuid.New()); err != nil {
		t.Fatal(err)
	}

	var rep archive.DeleteReport
	del.Report = func(r archive.DeleteReport) { rep = r }
	n, err := del.Run(ctx)
	if n != 2 || err == nil {
		t.Fatalf("deletion run: %d, %v; want 2 requests done and the stray one's error", n, err)
	}
	if rep.Requests != 2 || rep.Failed != 1 || rep.Overdue != 1 || rep.Objects != 4 {
		t.Errorf("report %+v; want 2 done, 1 failed, 1 overdue, 4 objects", rep)
	}
	if len(objects(gone.uid)) != 0 {
		t.Error("the deleted account's objects remain")
	}
	left := objects(reset.uid)
	if len(left) != 2 || !bytes.Contains([]byte(left[0].Key+left[1].Key), []byte(archive.MonthString(month(6)))) {
		t.Errorf("after the reset's deletion: %+v; want only the new archive (object + manifest)", left)
	}
	if len(objects(other.uid)) != 2 {
		t.Error("the stray request reached another user's objects")
	}
	if count(`SELECT count(*) FROM archive_deletions WHERE done_at IS NOT NULL AND done_at < NOW() - interval '30 days'`) != 0 {
		t.Error("a request finished 31 days ago was not purged")
	}
	if count(`SELECT count(*) FROM archive_deletions WHERE user_id = $1 AND done_at IS NULL AND attempts = 1 AND last_error IS NOT NULL`, stray) != 1 {
		t.Error("the stray request isn't pending with its error")
	}
	// The stale object doesn't open with the user's new key.
	var sealed []byte
	if err := pool.QueryRowContext(ctx, `SELECT sealed_key FROM archive_keys WHERE user_id = $1`, reset.uid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	key, err := archive.OpenDataKey(ctx, sealer, reset.uid, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ReadStats(bytes.NewReader(staleObj), key, archive.ObjectAAD(reset.uid, reset.vid, archive.MonthString(month(5))), []string{"at"}); !errors.Is(err, archive.ErrCorrupt) {
		t.Errorf("stale object with the new key: %v, want ErrCorrupt", err)
	}
}
