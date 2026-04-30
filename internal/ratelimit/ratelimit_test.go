// ratelimit tests run against miniredis. The Lua script logic is
// the bulk of the behaviour, so testing through real Lua execution
// (gopher-lua via miniredis) catches arithmetic/edge-case bugs in
// the script itself, not just the Go wrapper.
package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLimiter(t *testing.T, cfg Config) (*Limiter, *miniredis.Miniredis, *fakeClock) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg.Now = clk.now
	l, err := New(context.Background(), rdb, cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l, mr, clk
}

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func TestAllowDrainsBucketThenRejects(t *testing.T) {
	t.Parallel()
	l, _, _ := newTestLimiter(t, Config{
		MainCapacity:     3,
		MainRefillPerSec: 0.0001, // effectively zero refill within test
	})
	for i := 0; i < 3; i++ {
		ok, _ := l.Allow(context.Background(), ClassMain)
		if !ok {
			t.Fatalf("Allow %d: expected ok", i)
		}
	}
	ok, retry := l.Allow(context.Background(), ClassMain)
	if ok {
		t.Fatal("4th Allow should reject (bucket drained)")
	}
	if retry <= 0 {
		t.Fatalf("retryAfter = %v, want >0", retry)
	}
}

func TestAllowRefillsOverTime(t *testing.T) {
	t.Parallel()
	l, _, clk := newTestLimiter(t, Config{
		MainCapacity:     2,
		MainRefillPerSec: 1, // 1 token/sec
	})
	// Drain.
	_, _ = l.Allow(context.Background(), ClassMain)
	_, _ = l.Allow(context.Background(), ClassMain)
	if ok, _ := l.Allow(context.Background(), ClassMain); ok {
		t.Fatal("expected reject after drain")
	}
	// 1.5s later we should have ~1 token.
	clk.advance(1500 * time.Millisecond)
	if ok, _ := l.Allow(context.Background(), ClassMain); !ok {
		t.Fatal("expected accept after 1.5s refill")
	}
	// Bucket should be empty again.
	if ok, _ := l.Allow(context.Background(), ClassMain); ok {
		t.Fatal("expected reject after re-drain")
	}
}

func TestAllowClassesAreIndependent(t *testing.T) {
	t.Parallel()
	// Regression for the priority-can't-starve invariant: even if
	// main is fully drained, priority should serve.
	l, _, _ := newTestLimiter(t, Config{
		MainCapacity:         2,
		MainRefillPerSec:     0.0001,
		PriorityCapacity:     2,
		PriorityRefillPerSec: 0.0001,
	})
	// Drain main entirely.
	_, _ = l.Allow(context.Background(), ClassMain)
	_, _ = l.Allow(context.Background(), ClassMain)
	if ok, _ := l.Allow(context.Background(), ClassMain); ok {
		t.Fatal("main should be drained")
	}
	// Priority should still have full budget.
	if ok, _ := l.Allow(context.Background(), ClassPriority); !ok {
		t.Fatal("priority bucket should be independent")
	}
}

func TestAllowFailsOpenOnClosedClient(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	l, err := New(context.Background(), rdb, Config{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mr.Close() // simulate Redis going away
	// Fail-open: a Redis blip must NOT black-hole the upstream.
	// The breaker still gates real 429s.
	ok, _ := l.Allow(context.Background(), ClassMain)
	if !ok {
		t.Fatal("Allow should fail-open when Redis is unreachable")
	}
}

func TestRetryAfterRoughlyTracksRefill(t *testing.T) {
	t.Parallel()
	l, _, _ := newTestLimiter(t, Config{
		MainCapacity:     1,
		MainRefillPerSec: 1, // 1 token/sec → retry ~1s after drain
	})
	_, _ = l.Allow(context.Background(), ClassMain)
	ok, retry := l.Allow(context.Background(), ClassMain)
	if ok {
		t.Fatal("expected reject")
	}
	// Allow ±200ms of slack: Lua math.ceil + integer ms rounding.
	if retry < 800*time.Millisecond || retry > 1200*time.Millisecond {
		t.Fatalf("retryAfter = %v, want ~1s", retry)
	}
}
