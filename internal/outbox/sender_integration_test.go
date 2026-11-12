//go:build integration

package outbox

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func (e *env) state(t *testing.T, key string) (attempts int, sent, failed bool) {
	t.Helper()
	var sa, fa sql.NullTime
	if err := e.pool.QueryRowContext(e.ctx, `SELECT attempts, sent_at, failed_at FROM outbox WHERE key = $1`, key).Scan(&attempts, &sa, &fa); err != nil {
		t.Fatal(err)
	}
	return attempts, sa.Valid, fa.Valid
}

func TestSenderWithNoHandlerClaimsNothing(t *testing.T) {
	e := setup(t)
	k := "nh-" + uuid.NewString()
	e.insert(t, e.ctx, k)
	snd := NewSender(e.s, nil)
	if n, err := snd.Once(e.ctx); err != nil || n != 0 {
		t.Fatalf("Once = %d, %v", n, err)
	}
	if a, sent, failed := e.state(t, k); a != 0 || sent || failed {
		t.Fatalf("an unhandled row was touched: attempts=%d sent=%v failed=%v", a, sent, failed)
	}
}

func TestSenderOutcomes(t *testing.T) {
	e := setup(t)
	ok, retry, moot, old := "so-ok-"+uuid.NewString(), "so-retry-"+uuid.NewString(), "so-moot-"+uuid.NewString(), "so-old-"+uuid.NewString()
	for _, k := range []string{ok, retry, moot, old} {
		e.insert(t, e.ctx, k)
	}
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE outbox SET created_at = now() - interval '2 days' WHERE key = $1`, old); err != nil {
		t.Fatal(err)
	}
	var calls sync.Map
	snd := NewSender(e.s, nil)
	snd.Register(KindCloseHook, func(_ context.Context, r Row) error {
		calls.Store(r.Key, true)
		switch r.Key {
		case retry:
			return errors.New("provider says no")
		case moot:
			return ErrMoot
		}
		return nil
	})
	if n, err := snd.Once(e.ctx); err != nil || n != 4 {
		t.Fatalf("Once = %d, %v", n, err)
	}
	if _, ran := calls.Load(old); ran {
		t.Error("a row past its MaxAge reached the handler")
	}
	for k, want := range map[string][3]any{ok: {1, true, false}, retry: {1, false, false}, moot: {1, false, true}, old: {1, false, true}} {
		a, sent, failed := e.state(t, k)
		if a != want[0] || sent != want[1] || failed != want[2] {
			t.Errorf("%s: attempts=%d sent=%v failed=%v, want %v", k, a, sent, failed, want)
		}
	}
	// The retried row is backed off, so the next tick does not take it.
	if n, _ := snd.Once(e.ctx); n != 0 {
		t.Errorf("the next tick took %d rows, want 0 (backoff)", n)
	}
}

func TestTwoSendersNeverDoubleSend(t *testing.T) {
	e := setup(t)
	for i := 0; i < 40; i++ {
		e.insert(t, e.ctx, "ts-"+uuid.NewString())
	}
	var sends atomic.Int64
	h := func(context.Context, Row) error { sends.Add(1); return nil }
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		snd := NewSender(e.s, nil)
		snd.owner += "-" + string(rune('a'+i))
		snd.Batch = 5
		snd.Register(KindCloseHook, h)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if n, err := snd.Once(e.ctx); err != nil || n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	if sends.Load() != 40 || e.count(t, `sent_at IS NOT NULL`) != 40 {
		t.Fatalf("handled %d, sent rows %d, want 40 each", sends.Load(), e.count(t, `sent_at IS NOT NULL`))
	}
}

func TestJanitorPurgesInBatchesAndExpiresPending(t *testing.T) {
	e := setup(t)
	if _, err := e.pool.ExecContext(e.ctx, `
		INSERT INTO outbox (key, user_id, vehicle_id, kind, created_at, sent_at, failed_at)
		SELECT 'jn-' || g, $1, $2, 'close_hook', now() - interval '40 days',
		       CASE WHEN g % 3 = 0 THEN now() - interval '8 days' END,
		       CASE WHEN g % 3 = 1 THEN now() - interval '31 days' END
		  FROM generate_series(1, 2500) g`, e.user, e.veh); err != nil {
		t.Fatal(err)
	}
	e.insert(t, e.ctx, "jn-young")
	// close_hook MaxAge 24 h + 1 d grace: 30 h old survives, 50 h old expires.
	e.insert(t, e.ctx, "jn-30h")
	e.insert(t, e.ctx, "jn-50h")
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE outbox SET created_at = now() - CASE key WHEN 'jn-30h' THEN interval '30 hours' ELSE interval '50 hours' END WHERE key IN ('jn-30h','jn-50h')`); err != nil {
		t.Fatal(err)
	}
	j := NewJanitor(e.pool, e.s, nil)
	j.BatchSize, j.MaxBatch, j.Pause = 1000, 2, time.Millisecond
	if err := j.Once(e.ctx); err != nil {
		t.Fatal(err)
	}
	if left := e.count(t, `key LIKE 'jn-%'`); left != 2503-2000 {
		t.Fatalf("after 2 batches of 1000: %d left, want %d (the run is capped, the next continues)", left, 503)
	}
	if err := j.Once(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `key IN ('jn-young','jn-30h')`) != 2 || e.count(t, `key LIKE 'jn-%'`) != 2 {
		t.Fatal("only the two pending rows inside MaxAge + 1 d should remain")
	}
}

func TestJanitorSingletonSkipsSecondRunner(t *testing.T) {
	e := setup(t)
	// Hold the janitor's lock on a side connection; a tick must not purge.
	conn, err := e.pool.Conn(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(e.ctx, `SELECT pg_advisory_lock(hashtextextended('rivolt:outbox_purge', 0))`); err != nil {
		t.Fatal(err)
	}
	k := "sk-" + uuid.NewString()
	e.insert(t, e.ctx, k)
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE outbox SET sent_at = now() - interval '9 days' WHERE key = $1`, k); err != nil {
		t.Fatal(err)
	}
	if err := NewJanitor(e.pool, e.s, nil).Once(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `key = $1`, k) != 1 {
		t.Fatal("a second runner purged while the lock was held")
	}
}
