package api

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type failPods struct {
	mr       *miniredis.Miniredis
	pods     []*failLimiter
	fallback atomic.Int32
	lockouts atomic.Int32
	clock    *time.Time
}

// newFailPods builds n replicas of one limiter (10 failures per 15 minutes) over one Redis.
func newFailPods(t *testing.T, n int) *failPods {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	now := time.Unix(1_700_000_000, 0)
	fp := &failPods{mr: mr, clock: &now}
	for i := 0; i < n; i++ {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		l := newFailLimiter(10, 15*time.Minute).withRedis(rdb, "rivolt", "login", func() { fp.fallback.Add(1) })
		l.now = func() time.Time { return *fp.clock }
		l.shared.now = l.now
		l.onLockout = func() { fp.lockouts.Add(1) }
		fp.pods = append(fp.pods, l)
	}
	return fp
}

func TestFailuresAreCountedAcrossReplicas(t *testing.T) {
	fp := newFailPods(t, 3)
	a, b, c := fp.pods[0], fp.pods[1], fp.pods[2]
	key := emailLimitKey("driver@example.invalid")
	for i := 0; i < 4; i++ {
		a.fail(key)
	}
	for i := 0; i < 3; i++ {
		b.fail(key)
	}
	for _, p := range fp.pods {
		if p.blocked(key) != 0 {
			t.Fatal("blocked at 7 failures")
		}
	}
	for i := 0; i < 3; i++ {
		c.fail(key) // 10 across the three; none of them has more than 4 itself
	}
	for i, p := range fp.pods {
		if w := p.blocked(key); w <= 0 || w > 15*time.Minute {
			t.Fatalf("replica %d: wait %v after 10 failures across replicas, want 0 < wait <= 15m", i, w)
		}
	}
	if n := fp.lockouts.Load(); n != 1 {
		t.Fatalf("lockout counted %d times, want once for the key reaching the limit", n)
	}
	// A right password on one replica frees the key on all.
	b.reset(key)
	for i, p := range fp.pods {
		if p.blocked(key) != 0 {
			t.Fatalf("replica %d still blocks after a reset elsewhere", i)
		}
	}
	// Another address was never affected.
	if a.blocked(emailLimitKey("other@example.invalid")) != 0 {
		t.Fatal("an unrelated key is blocked")
	}
}

func TestSharedFailuresExpireWithTheWindow(t *testing.T) {
	fp := newFailPods(t, 2)
	key := emailLimitKey("driver@example.invalid")
	for i := 0; i < 10; i++ {
		fp.pods[i%2].fail(key)
	}
	if fp.pods[0].blocked(key) <= 0 {
		t.Fatal("setup: not blocked")
	}
	*fp.clock = fp.clock.Add(15*time.Minute + time.Second)
	for i, p := range fp.pods {
		if p.blocked(key) != 0 {
			t.Fatalf("replica %d still blocks after the window", i)
		}
	}
}

func TestOnlyHashedKeysReachRedis(t *testing.T) {
	fp := newFailPods(t, 1)
	fp.pods[0].fail("passkey-signin:203.0.113.9")
	fp.pods[0].fail("driver@example.invalid")
	keys := fp.mr.Keys()
	if len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "rivolt:fail:login:") || strings.Contains(k, "203.0.113") || strings.Contains(k, "example") {
			t.Fatalf("key %q is not a prefixed hash", k)
		}
	}
}

// Redis down: never fail open past the per-pod limit, never block on an error, and say so.
func TestRedisOutageFallsBackToThePerPodCountAndNeverLocksAnyoneOut(t *testing.T) {
	fp := newFailPods(t, 3)
	key := emailLimitKey("driver@example.invalid")
	fp.mr.Close()
	a := fp.pods[0]
	if a.blocked(key) != 0 {
		t.Fatal("an unreachable Redis blocked someone")
	}
	if fp.fallback.Load() == 0 {
		t.Fatal("the fallback was not counted")
	}
	// Per pod, the limit still holds: 10 failures on one replica block it.
	for i := 0; i < 9; i++ {
		a.fail(key)
		if a.blocked(key) != 0 {
			t.Fatalf("blocked after %d failures", i+1)
		}
	}
	a.fail(key)
	if a.blocked(key) <= 0 {
		t.Fatal("10 failures on one replica did not block it while Redis was down (failed open)")
	}
	// Another replica that has seen none is not locked out by it.
	if fp.pods[1].blocked(key) != 0 {
		t.Fatal("a replica with no failures of its own was blocked by an outage")
	}
	// The lockout is still counted once (from the per-pod count).
	if n := fp.lockouts.Load(); n != 1 {
		t.Fatalf("lockouts = %d, want 1", n)
	}
}

// After a failure Redis is left alone for the backoff (one slow call, not one per login),
// then tried again and used once it answers.
func TestRedisIsRetriedAfterTheBackoff(t *testing.T) {
	fp := newFailPods(t, 2)
	key := emailLimitKey("driver@example.invalid")
	fp.mr.SetError("LOADING")
	fp.pods[0].blocked(key)
	n := fp.fallback.Load()
	if n == 0 {
		t.Fatal("setup: no fallback counted")
	}
	for i := 0; i < 5; i++ {
		fp.pods[0].blocked(key) // within the backoff: not even tried
	}
	if fp.fallback.Load() != n {
		t.Fatalf("Redis was retried inside the backoff (%d -> %d fallbacks)", n, fp.fallback.Load())
	}
	fp.mr.SetError("")
	*fp.clock = fp.clock.Add(sharedFailBackoff + time.Second)
	for i := 0; i < 10; i++ {
		fp.pods[1].fail(key)
	}
	if fp.pods[0].blocked(key) <= 0 {
		t.Fatal("replica 0 does not use Redis again after the backoff")
	}
}

// Without Redis nothing changes: the limiter is per pod, as before.
func TestFailLimiterWithoutRedisIsPerPod(t *testing.T) {
	l := newFailLimiter(3, time.Minute).withRedis(nil, "rivolt", "login", nil)
	if l.shared != nil {
		t.Fatal("a nil Redis must leave the limiter per pod")
	}
	for i := 0; i < 3; i++ {
		l.fail("k")
	}
	if l.blocked("k") <= 0 {
		t.Fatal("not blocked after 3 failures")
	}
}
