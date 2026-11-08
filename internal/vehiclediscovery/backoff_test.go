package vehiclediscovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// The backoff is shared across pods through Redis, under the install prefix, named by an HMAC of the user id (never the id), with the backoff as
// its TTL; without Redis it is held in memory.
func TestRedisBackoffHoldsAcrossPodsWithoutTheUserID(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	uid := uuid.New()
	a, b := NewRedisBackoff(rdb, "rivolt", []byte("limiter-key")), NewRedisBackoff(rdb, "rivolt", []byte("limiter-key"))
	if a.Held(ctx, uid) {
		t.Fatal("held before anything was recorded")
	}
	a.Hold(ctx, uid, EmptyBackoff)
	if !b.Held(ctx, uid) {
		t.Error("another pod does not see the hold")
	}
	keys := mr.Keys()
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "rivolt:vdisc:") || strings.Contains(keys[0], uid.String()) {
		t.Errorf("keys = %v, want one under rivolt:vdisc: that does not name the user", keys)
	}
	if ttl := mr.TTL(keys[0]); ttl < EmptyBackoff-time.Minute || ttl > EmptyBackoff {
		t.Errorf("ttl = %v, want about %v", ttl, EmptyBackoff)
	}
	mr.FastForward(EmptyBackoff + time.Second)
	if (&RedisBackoff{}).usable() {
		t.Error("a zero backoff must not use Redis")
	}
	fresh := NewRedisBackoff(rdb, "rivolt", []byte("limiter-key"))
	if fresh.Held(ctx, uid) {
		t.Error("still held after the backoff passed")
	}
}

func TestBackoffWithoutRedisIsPerPodMemory(t *testing.T) {
	b := NewRedisBackoff(nil, "rivolt", nil)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	uid := uuid.New()
	ctx := context.Background()
	b.Hold(ctx, uid, ErrorBackoff)
	if !b.Held(ctx, uid) {
		t.Fatal("not held in memory")
	}
	now = now.Add(ErrorBackoff + time.Second)
	if b.Held(ctx, uid) {
		t.Error("still held after the backoff")
	}
}
