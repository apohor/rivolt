//go:build integration

package outbox

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

type env struct {
	ctx  context.Context
	pool *sql.DB
	s    *Store
	user uuid.UUID
	veh  uuid.UUID
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	u, err := db.EnsureUser(ctx, pool, "ob-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	var v uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,$2,'R1S') RETURNING id`, u, "ob-"+uuid.NewString()).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return &env{ctx: ctx, pool: pool, s: NewStore(pool, nil), user: u, veh: v}
}

func (e *env) insert(t *testing.T, ctx context.Context, key string) bool {
	t.Helper()
	tx, err := e.pool.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := e.s.Insert(ctx, tx, Intent{Key: key, UserID: e.user, VehicleID: e.veh, Kind: KindCloseHook, Payload: []byte(`{"n":1}`)})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ok
}

func (e *env) count(t *testing.T, where string, args ...any) (n int) {
	t.Helper()
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM outbox WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInsertIsIdempotentAndRollsBack(t *testing.T) {
	e := setup(t)
	k := "idem-" + uuid.NewString()
	if !e.insert(t, e.ctx, k) || e.insert(t, e.ctx, k) {
		t.Fatal("first insert must write, the repeat must not")
	}
	if e.count(t, `key = $1`, k) != 1 {
		t.Fatal("want one row")
	}
	// Rolled back with the caller's transaction.
	tx, _ := e.pool.BeginTx(e.ctx, nil)
	rk := "rb-" + uuid.NewString()
	if ok, err := e.s.Insert(e.ctx, tx, Intent{Key: rk, UserID: e.user, VehicleID: e.veh, Kind: KindPush}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	_ = tx.Rollback()
	if e.count(t, `key = $1`, rk) != 0 {
		t.Fatal("a rolled-back insert left a row")
	}
	// A suppressed run records nothing.
	sk := "sup-" + uuid.NewString()
	if e.insert(t, WithSuppressed(e.ctx), sk) || e.count(t, `key = $1`, sk) != 0 {
		t.Fatal("a suppressed insert wrote a row")
	}
}

func TestClaimIsExclusiveAndLeaseExpires(t *testing.T) {
	e := setup(t)
	for i := 0; i < 20; i++ {
		e.insert(t, e.ctx, "excl-"+uuid.NewString())
	}
	_, _ = e.pool.ExecContext(e.ctx, `UPDATE outbox SET sent_at = now() WHERE key NOT LIKE 'excl-%' AND sent_at IS NULL`)
	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for _, owner := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				rows, err := e.s.Claim(e.ctx, owner, 3, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if len(rows) == 0 {
					return
				}
				mu.Lock()
				for _, r := range rows {
					if prev, dup := seen[r.ID]; dup {
						t.Errorf("row %d claimed by %s and %s", r.ID, prev, owner)
					}
					seen[r.ID] = owner
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) < 20 {
		t.Fatalf("claimed %d rows, want at least 20", len(seen))
	}
	// An expired lease is reclaimable and the stale owner cannot mark it once someone else holds it.
	k := "lease-" + uuid.NewString()
	e.insert(t, e.ctx, k)
	var id int64
	_ = e.pool.QueryRowContext(e.ctx, `SELECT id FROM outbox WHERE key = $1`, k).Scan(&id)
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE outbox SET sent_at = now() WHERE sent_at IS NULL AND id <> $1`, id); err != nil {
		t.Fatal(err)
	}
	if rows, _ := e.s.Claim(e.ctx, "old", 5, time.Minute); len(rows) != 1 {
		t.Fatalf("claim = %d rows, want 1", len(rows))
	}
	if rows, _ := e.s.Claim(e.ctx, "new", 5, time.Minute); len(rows) != 0 {
		t.Fatal("a leased row was claimed twice")
	}
	_, _ = e.pool.ExecContext(e.ctx, `UPDATE outbox SET claimed_until = now() - interval '1 second' WHERE id = $1`, id)
	// Expired but not reclaimed: the original sender finishing late is recorded, not sent again.
	if err := e.s.MarkSent(e.ctx, id, "old"); err != nil {
		t.Fatalf("late MarkSent before any reclaim: %v", err)
	}
	if err := e.s.MarkSent(e.ctx, id, "old"); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("second MarkSent = %v, want ErrNotClaimed", err)
	}
	// Reclaimed by another: the old owner may not mark.
	k2 := "lease2-" + uuid.NewString()
	e.insert(t, e.ctx, k2)
	rows, _ := e.s.Claim(e.ctx, "old", 5, time.Minute)
	if len(rows) != 1 {
		t.Fatalf("claim = %d", len(rows))
	}
	_, _ = e.pool.ExecContext(e.ctx, `UPDATE outbox SET claimed_until = now() - interval '1 second' WHERE id = $1`, rows[0].ID)
	if r2, _ := e.s.Claim(e.ctx, "new", 5, time.Minute); len(r2) != 1 || r2[0].Attempts != 2 {
		t.Fatalf("reclaim = %+v", r2)
	}
	if err := e.s.MarkSent(e.ctx, rows[0].ID, "old"); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("stale owner MarkSent = %v, want ErrNotClaimed", err)
	}
}

func TestReleaseBackoffAndPoisonRow(t *testing.T) {
	e := setup(t)
	k := "bo-" + uuid.NewString()
	e.insert(t, e.ctx, k)
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE outbox SET sent_at = now() WHERE sent_at IS NULL AND key <> $1`, k); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.s.Claim(e.ctx, "w", 1, time.Minute)
	if len(rows) != 1 {
		t.Fatal("no claim")
	}
	id := rows[0].ID
	if err := e.s.Release(e.ctx, id, "w"); err != nil {
		t.Fatal(err)
	}
	// The backoff is honoured: the released row is not due.
	if rows, _ := e.s.Claim(e.ctx, "w", 1, time.Minute); len(rows) != 0 {
		t.Fatal("a backed-off row was claimed")
	}
	var wait float64
	_ = e.pool.QueryRowContext(e.ctx, `SELECT extract(epoch FROM claimed_until - now()) FROM outbox WHERE id = $1`, id).Scan(&wait)
	if wait < 20 || wait > 31 {
		t.Fatalf("first backoff %.0f s, want about 30", wait)
	}
	// Poison: a row that is claimed MaxAttempts times and never released comes back no more, and is set aside by Claim itself.
	_, _ = e.pool.ExecContext(e.ctx, `UPDATE outbox SET attempts = $2, claimed_by = NULL, claimed_until = now() - interval '1 second' WHERE id = $1`, id, MaxAttempts)
	if rows, _ := e.s.Claim(e.ctx, "w", 1, time.Minute); len(rows) != 0 {
		t.Fatal("a row at the attempt cap was returned")
	}
	if e.count(t, `id = $1 AND failed_at IS NOT NULL`, id) != 1 {
		t.Fatal("Claim did not set the poison row aside")
	}
	// Release at the cap also sets it aside.
	k2 := "cap-" + uuid.NewString()
	e.insert(t, e.ctx, k2)
	var id2 int64
	_ = e.pool.QueryRowContext(e.ctx, `SELECT id FROM outbox WHERE key = $1`, k2).Scan(&id2)
	_, _ = e.pool.ExecContext(e.ctx, `UPDATE outbox SET attempts = $2 - 1 WHERE id = $1`, id2, MaxAttempts)
	if rows, _ := e.s.Claim(e.ctx, "w", 1, time.Minute); len(rows) != 1 || rows[0].Attempts != MaxAttempts {
		t.Fatalf("claim at cap-1 = %+v", rows)
	}
	if err := e.s.Release(e.ctx, id2, "w"); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `id = $1 AND failed_at IS NOT NULL`, id2) != 1 {
		t.Fatal("Release at the cap did not set the row aside")
	}
}

func TestPurgeAndCascade(t *testing.T) {
	e := setup(t)
	mk := func(prefix, set string) int64 {
		k := prefix + uuid.NewString()
		e.insert(t, e.ctx, k)
		var id int64
		_ = e.pool.QueryRowContext(e.ctx, `UPDATE outbox SET `+set+` WHERE key = $1 RETURNING id`, k).Scan(&id)
		return id
	}
	oldSent := mk("ps-", `sent_at = now() - interval '8 days'`)
	newSent := mk("ps-", `sent_at = now() - interval '1 day'`)
	oldFail := mk("pf-", `failed_at = now() - interval '31 days'`)
	midFail := mk("pf-", `failed_at = now() - interval '10 days'`)
	pending := mk("pp-", `created_at = now() - interval '40 days'`)
	young := mk("pn-", `created_at = now() - interval '1 day'`)
	now := time.Now()
	n, err := e.s.PurgeBatch(e.ctx, now.Add(-SentTTL), now.Add(-FailedTTL), 1000)
	if err != nil || n.Sent < 1 || n.Failed < 1 || n.Expired < 1 {
		t.Fatalf("purge = %+v, %v", n, err)
	}
	for id, want := range map[int64]int{oldSent: 0, newSent: 1, oldFail: 0, midFail: 1, pending: 0, young: 1} {
		if got := e.count(t, `id = $1`, id); got != want {
			t.Errorf("row %d: %d rows after purge, want %d", id, got, want)
		}
	}
	// Account deletion takes the user's rows (and a vehicle's go with the vehicle).
	if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM vehicles WHERE id = $1`, e.veh); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `user_id = $1 AND vehicle_id IS NOT NULL`, e.user) != 0 {
		t.Fatal("vehicle rows survived the vehicle")
	}
	tx, _ := e.pool.BeginTx(e.ctx, nil)
	k := "acct-" + e.user.String()
	if ok, err := e.s.Insert(e.ctx, tx, Intent{Key: k, UserID: e.user, Kind: KindEmail}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	_ = tx.Commit()
	if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM users WHERE id = $1`, e.user); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `user_id = $1`, e.user) != 0 {
		t.Fatal("rows survived the account")
	}
}
