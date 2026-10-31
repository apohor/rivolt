package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// sharedBuckets is the Redis half of the per-IP/per-user token buckets (ipLimiter): one bucket per
// limiter and key counted across every replica, so the limit is the limit and not "limit x pods".
// An ipLimiter always keeps its own per-pod bucket as well (it is spent on every request Redis
// allows), so the fallback is warm when Redis goes away.
//
// What it never does:
//   - fail open: when Redis cannot answer, the per-pod bucket decides (the limit is then up to the
//     limit per pod, as before Redis was used, never no limit), and the fallback is counted;
//   - refuse because of an error: only a successful answer from the script refuses a request.
//
// The bucket lives in a Lua script and measures time with Redis's own clock, so replicas with
// skewed clocks agree. Keys are an HMAC-SHA256 of the limiter name and the key (an address, a user id),
// keyed with a subkey of the server's secret (RateLimiterKey), under the install's key prefix: an
// address never reaches Redis, and the names cannot be matched against guessed addresses by anyone who
// can read Redis but does not hold the secret. A Redis outage costs one slow call per backoff, not one
// per request.
type sharedBuckets struct {
	rdb    *redis.Client
	prefix string
	key    []byte
	now    func() time.Time
	// onFallback counts every call that fell back to the per-pod bucket, by limiter name.
	onFallback func(limiter string)
	// onName is told every limiter name as it is built, so its fallback series can exist at 0 from the start (an increase() alert
	// misses the first fallback of a series that did not exist before it).
	onName func(limiter string)
	// downUntil (unix nanos): Redis is left alone until then after a failure.
	downUntil atomic.Int64
}

const (
	sharedBucketOpTimeout = 250 * time.Millisecond
	sharedBucketBackoff   = 15 * time.Second
)

// RateLimiterKey derives the HMAC key for the rate limiters' Redis key names from the failure limiter's
// subkey (a purpose-labelled subkey again, never the secret itself). nil for an empty key.
func RateLimiterKey(failKey []byte) []byte {
	if len(failKey) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, failKey)
	m.Write([]byte("rivolt/rate-limiter/redis-keys/v1"))
	return m.Sum(nil)
}

// tokenBucketScript takes cost tokens from a bucket of burst tokens refilled at rate tokens per
// millisecond, by Redis's clock. It returns {allowed, waitMs}: wait is the time until the bucket holds
// the tokens (for a cost larger than the burst, the time it would take a full refill of that cost).
var tokenBucketScript = redis.NewScript(`
pcall(redis.replicate_commands)
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
if cost > burst then
  return {0, math.ceil(cost / rate)}
end
local h = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(h[1])
local ts = tonumber(h[2])
if tokens == nil or ts == nil then
  tokens = burst
  ts = now
end
tokens = math.min(burst, tokens + math.max(0, now - ts) * rate)
local allowed = 0
local wait = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
else
  wait = math.ceil((cost - tokens) / rate)
end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'ts', now)
redis.call('PEXPIRE', KEYS[1], ttl)
return {allowed, wait}
`)

func (s *sharedBuckets) usable() bool {
	return s != nil && s.rdb != nil && len(s.key) > 0 && s.now().UnixNano() >= s.downUntil.Load()
}

func (s *sharedBuckets) named(limiter string) {
	if s != nil && s.onName != nil {
		s.onName(limiter)
	}
}

func (s *sharedBuckets) failed(limiter string) {
	s.downUntil.Store(s.now().Add(sharedBucketBackoff).UnixNano())
	s.fellBack(limiter)
}

func (s *sharedBuckets) fellBack(limiter string) {
	if s.onFallback != nil {
		s.onFallback(limiter)
	}
}

func (s *sharedBuckets) redisKey(limiter, key string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(limiter + "\x00" + key))
	return s.prefix + ":rl:" + limiter + ":" + hex.EncodeToString(m.Sum(nil)[:16])
}

// allow takes n tokens from key's bucket in limiter. answered is false when Redis could not say
// (the caller's own bucket decides); otherwise ok is the verdict and wait how long a refused request
// should wait.
func (s *sharedBuckets) allow(limiter, key string, perSecond float64, burst, n int) (ok bool, wait time.Duration, answered bool) {
	if s == nil || s.rdb == nil || len(s.key) == 0 {
		return false, 0, false
	}
	if !s.usable() {
		// In backoff: every call served per pod is a fallback worth counting.
		s.fellBack(limiter)
		return false, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedBucketOpTimeout)
	defer cancel()
	perMs := perSecond / 1000
	// Idle for a full refill, plus a minute, and the entry goes away.
	ttl := time.Duration(float64(burst)/perSecond*float64(time.Second)) + time.Minute
	res, err := tokenBucketScript.Run(ctx, s.rdb, []string{s.redisKey(limiter, key)}, perMs, burst, n, ttl.Milliseconds()).Int64Slice()
	if err != nil || len(res) != 2 {
		s.failed(limiter)
		return false, 0, false
	}
	return res[0] == 1, time.Duration(res[1]) * time.Millisecond, true
}

// limiterSet builds the router's limiters over one shared-bucket backend; a nil set (no Redis, or a
// test) builds plain per-pod limiters.
type limiterSet struct {
	shared *sharedBuckets
}

// limiterSet is the set for a router: Redis-backed when there is a client and a key, else per pod.
func (d Deps) limiterSet() *limiterSet {
	if d.Redis == nil || len(d.FailLimiterKey) == 0 {
		return nil
	}
	sb := &sharedBuckets{rdb: d.Redis, prefix: d.RedisPrefix, key: RateLimiterKey(d.FailLimiterKey), now: time.Now}
	if d.Metrics != nil {
		sb.onFallback = func(name string) { d.Metrics.RateLimiterFallbackTotal.WithLabelValues(name).Inc() }
		sb.onName = func(name string) { d.Metrics.RateLimiterFallbackTotal.WithLabelValues(name).Add(0) }
	}
	return &limiterSet{shared: sb}
}

// ip is newIPLimiter with a name: the Redis bucket's identity. A name must be unique per limiter (two
// routes with different limits must not share one) and stable across releases.
func (s *limiterSet) ip(name string, perWindow int, window, idleTTL time.Duration) *ipLimiter {
	l := newIPLimiter(perWindow, window, idleTTL)
	l.name = name
	if s != nil {
		l.shared = s.shared
		s.shared.named(name)
	}
	return l
}

// user is mapsUserLimiter with a name: a per-minute bucket per signed-in user (per address when signed out).
func (s *limiterSet) user(name string, perMin int) *ipLimiter {
	l := mapsUserLimiter(perMin)
	l.name = name
	if s != nil {
		l.shared = s.shared
		s.shared.named(name)
	}
	return l
}
