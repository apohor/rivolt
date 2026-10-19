//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/archive/s3"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
)

// gatedPut holds the first object PUT until released, and says when it is
// being held.
type gatedPut struct {
	next    http.RoundTripper
	once    sync.Once
	held    chan struct{}
	release chan struct{}
}

func (g *gatedPut) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut && bytes.HasSuffix([]byte(r.URL.Path), []byte(".csv.gz.enc")) {
		first := false
		g.once.Do(func() { first = true })
		if first {
			close(g.held)
			<-g.release
		}
	}
	return g.next.RoundTrip(r)
}

// TestArchiveUserLock: an account deletion or history reset that arrives
// while the archive job is writing one of the user's objects waits for it
// (the per-user archive lock), so its deletion request is newer than the
// object and the deletion run removes it: no object outlives the request
// (DA on #609).
func TestArchiveUserLock(t *testing.T) {
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
	del, err := archive.NewDeleter(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -5, 0)
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, month); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		delete func(uuid.UUID) error
	}{
		{"account deletion", func(uid uuid.UUID) error { return db.DeleteUserAccount(ctx, pool, uid) }},
		{"history reset", func(uid uuid.UUID) error {
			// The handler's order: samples first, then the archive.
			if _, err := pool.ExecContext(ctx, `DELETE FROM vehicle_state WHERE user_id = $1`, uid); err != nil {
				return err
			}
			return db.ResetUserArchive(ctx, pool, uid)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, err := db.EnsureUserFull(ctx, pool, "lock-"+uuid.NewString()[:8], "", "Lock")
			if err != nil {
				t.Fatal(err)
			}
			var vid uuid.UUID
			if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`, uid, "rv-"+uid.String()[:8]).Scan(&vid); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'test')`, uid, vid, month.Add(30*time.Hour)); err != nil {
				t.Fatal(err)
			}
			gate := &gatedPut{next: http.DefaultTransport, held: make(chan struct{}), release: make(chan struct{})}
			c := cfg
			c.HTTP = &http.Client{Transport: gate, Timeout: time.Minute}
			arch, err := archive.NewArchiver(pool, c, sealer)
			if err != nil {
				t.Fatal(err)
			}
			runDone := make(chan error, 1)
			go func() { _, err := arch.Run(ctx); runDone <- err }()
			select {
			case <-gate.held:
			case <-time.After(time.Minute):
				t.Fatal("the archive job never reached the object PUT")
			}

			delDone := make(chan error, 1)
			go func() { delDone <- tc.delete(uid) }()
			select {
			case err := <-delDone:
				t.Fatalf("%s didn't wait for the object being written (err %v)", tc.name, err)
			case <-time.After(1500 * time.Millisecond):
			}
			close(gate.release)
			if err := <-runDone; err != nil {
				t.Fatalf("archive run: %v", err)
			}
			if err := <-delDone; err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			// The object written during the wait is older than the request.
			del.ClockMargin = 0
			if _, err := del.Run(ctx); err != nil {
				t.Fatal(err)
			}
			var left []string
			if err := bucket.List(ctx, archive.UserPrefix("test", uid), func(o s3.Object) error { left = append(left, o.Key); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(left) != 0 {
				t.Errorf("objects outlived the %s: %v", tc.name, left)
			}
			var months int
			if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM archive_months WHERE user_id = $1`, uid).Scan(&months); err != nil || months != 0 {
				t.Errorf("archive_months rows left after the %s: %d (%v)", tc.name, months, err)
			}
		})
	}
}
