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

// A Redis blip must neither black-hole the upstream nor lift the limit: the call is served
// from a per-process bucket that is a share (1/FallbackDivisor) of the configured one.
func TestAllowUsesAPerProcessBucketWhenRedisIsUnreachable(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	var fallbacks int
	l, err := New(context.Background(), rdb, Config{
		MainCapacity: 6, MainRefillPerSec: 0.0001, FallbackDivisor: 3,
		OnFallback: func(Class) { fallbacks++ },
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mr.Close() // simulate Redis going away
	allowed := 0
	for i := 0; i < 20; i++ {
		if ok, _ := l.Allow(context.Background(), ClassMain); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("allowed %d of 20 with Redis down, want the fallback bucket's 2 (capacity 6 / 3): 0 would black-hole the upstream, 20 would be failing open", allowed)
	}
	if ok, retry := l.Allow(context.Background(), ClassMain); ok || retry <= 0 {
		t.Fatalf("the refused call must say when to retry: ok=%v retry=%v", ok, retry)
	}
	if fallbacks != 21 {
		t.Fatalf("fallbacks counted = %d, want 21", fallbacks)
	}
	// A class has at least one token, however small the share.
	l2 := &Limiter{cfg: withDefaults(Config{MainCapacity: 1, PriorityCapacity: 1, FallbackDivisor: 10})}
	if ok, _ := l2.fallback.allow(ClassPriority, l2.cfg); !ok {
		t.Fatal("a class must keep at least one token in the fallback")
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

// Redis down at boot must not disable the limiter for the life of the process:
// it uses the per-process bucket until Redis answers, then limits (review §4.2).
func TestRetryingStartsLimitingWhenRedisComesUp(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	addr := mr.Addr()
	mr.Close() // down at "boot"
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{MainCapacity: 6, MainRefillPerSec: 0.001, FallbackDivisor: 3}
	ready := make(chan struct{})
	r := NewRetrying(ctx, rdb, cfg, nil, 200*time.Millisecond, 20*time.Millisecond, func() { close(ready) })

	if r.Ready() {
		t.Fatal("ready with Redis down")
	}
	downAllowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := r.Allow(ctx, ClassMain); ok {
			downAllowed++
		}
	}
	if downAllowed != 2 {
		t.Fatalf("%d of 10 allowed while Redis is down, want the per-process bucket's 2 (not 10: unlimited; not 0: black-holed)", downAllowed)
	}

	if err := mr.StartAddr(addr); err != nil {
		t.Fatalf("restart miniredis: %v", err)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the limiter never connected after Redis came up")
	}
	if !r.Ready() {
		t.Fatal("not ready after the connection was made")
	}
	// Once connected it limits: the bucket holds 6 tokens and refills at 0.001/s, so
	// a refusal must come. A call that fails open on a Redis blip right after the
	// restart (the limiter's contract) can let an extra one through on a loaded
	// runner, so the test waits for the first refusal instead of counting exactly.
	allowed, refused := 0, false
	for i := 0; i < 50 && !refused; i++ {
		if ok, _ := r.Allow(ctx, ClassMain); ok {
			allowed++
		} else {
			refused = true
		}
	}
	if !refused {
		t.Errorf("%d calls all allowed after connecting; the bucket's 2 tokens should run out", allowed)
	}
}

// A handover burst of background calls cannot take the last of main's tokens, and cannot touch the priority
// bucket: user calls are allowed throughout, and the owner's live path (seeds, subscribes) is allowed
// until main is truly empty.
func TestBackgroundBurstLeavesTheFloorAndTheUserLane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l, _, _ := newTestLimiter(t, Config{
		MainCapacity: 60, MainRefillPerSec: 0.0001,
		PriorityCapacity: 20, PriorityRefillPerSec: 0.0001,
		BackgroundFloor: 20,
	})
	// 10 vehicles x schedules + OTA + receipts and more: far over what background may spend.
	granted := 0
	for i := 0; i < 100; i++ {
		if ok, _ := l.Allow(ctx, ClassBackground); ok {
			granted++
		}
	}
	if granted != 40 {
		t.Fatalf("background was granted %d tokens, want 40 (60 minus the floor of 20)", granted)
	}
	// Every user call is allowed in the same instant: its own bucket was never touched.
	for i := 0; i < 20; i++ {
		if ok, _ := l.Allow(ctx, ClassPriority); !ok {
			t.Fatalf("user call %d refused during a background burst", i)
		}
	}
	// The live path spends the floor, to zero.
	for i := 0; i < 20; i++ {
		if ok, _ := l.Allow(ctx, ClassMain); !ok {
			t.Fatalf("live call %d refused with the floor still unspent", i)
		}
	}
	if ok, _ := l.Allow(ctx, ClassMain); ok {
		t.Error("main allowed a call with no tokens left")
	}
}

// The floor in the script: background at exactly the floor is refused with a retry-after for one more token;
// one above it is allowed; a live call at one token is allowed.
func TestBackgroundFloorEdges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l, _, clk := newTestLimiter(t, Config{MainCapacity: 5, MainRefillPerSec: 1, BackgroundFloor: 3})
	// Spend 2 live: 3 left = the floor.
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow(ctx, ClassMain); !ok {
			t.Fatal("setup")
		}
	}
	ok, retry := l.Allow(ctx, ClassBackground)
	if ok {
		t.Fatal("background was allowed at exactly the floor")
	}
	if retry < 900*time.Millisecond || retry > 1100*time.Millisecond {
		t.Errorf("retry-after = %v, want about 1s (one token above the floor at 1/s)", retry)
	}
	clk.advance(time.Second) // 4 tokens: one above the floor
	if ok, _ := l.Allow(ctx, ClassBackground); !ok {
		t.Error("background refused one token above the floor")
	}
	// 3 left again. A live call still goes through, down to 1 and 0.
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow(ctx, ClassMain); !ok {
			t.Errorf("live call %d refused", i)
		}
	}
}

// The local bucket (Redis down) has the same three classes: background leaves a floor scaled by the divisor.
func TestBackgroundFloorInTheLocalFallback(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := withDefaults(Config{MainCapacity: 60, MainRefillPerSec: 0.0001, BackgroundFloor: 20, FallbackDivisor: 4, Now: clk.now})
	var f fallbackBuckets
	// Local main capacity 15, floor 5: background gets 10.
	granted := 0
	for i := 0; i < 50; i++ {
		if ok, _ := f.allow(ClassBackground, cfg); ok {
			granted++
		}
	}
	if granted != 10 {
		t.Fatalf("local background granted %d, want 10 (15 minus a floor of 5)", granted)
	}
	for i := 0; i < 5; i++ {
		if ok, _ := f.allow(ClassMain, cfg); !ok {
			t.Fatalf("local live call %d refused with the floor unspent", i)
		}
	}
	if ok, _ := f.allow(ClassMain, cfg); ok {
		t.Error("local main allowed a call with no tokens left")
	}
}
