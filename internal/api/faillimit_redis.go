package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// sharedFails is the Redis half of a failLimiter: the failures of a key counted across
// every replica, so the limit is the limit and not "limit x pods". The failLimiter always
// keeps its own per-pod count as well, and uses this one in addition.
//
// What it never does:
//   - fail open: a Redis error leaves the per-pod count in force (the limit is then up to
//     max per pod, as before Redis was used, never no limit);
//   - lock anyone out: only a successful read of enough recent failures blocks, and an
//     error is never read as "blocked"; every entry expires with its window.
//
// Keys are an HMAC-SHA256 of the limiter name and the key, keyed with a subkey of the server's
// secret (see FailLimiterKey), under the install's key prefix: an address or an email never
// reaches Redis, and, being keyed, the names cannot be matched against a list of guessed
// addresses by anyone who can read Redis but does not hold the secret. A Redis outage costs
// one slow call per backoff, not one per login.
type sharedFails struct {
	rdb    *redis.Client
	prefix string
	name   string
	// key is the HMAC key (FailLimiterKey).
	key    []byte
	max    int
	window time.Duration
	now    func() time.Time
	// onFallback counts every call that fell back to the per-pod count.
	onFallback func()
	// downUntil (unix nanos): Redis is left alone until then after a failure.
	downUntil atomic.Int64
}

const (
	sharedFailOpTimeout = 250 * time.Millisecond
	sharedFailBackoff   = 15 * time.Second
)

// FailLimiterKey derives the HMAC key for the failure limiter's Redis key names from the
// server's secret (a purpose-labelled subkey, never the secret itself). nil for an empty secret.
func FailLimiterKey(secret []byte) []byte {
	if len(secret) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("rivolt/fail-limiter/redis-keys/v1"))
	return m.Sum(nil)
}

func (s *sharedFails) redisKey(key string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(s.name + "\x00" + key))
	return s.prefix + ":fail:" + s.name + ":" + hex.EncodeToString(m.Sum(nil)[:16])
}

// opContext bounds one Redis call. It carries no user: the limiter works on hashed keys, and
// Redis is not a user-scoped store.
func (s *sharedFails) opContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sharedFailOpTimeout)
}

// usable reports whether Redis may be tried now.
func (s *sharedFails) usable() bool {
	return s != nil && s.rdb != nil && s.now().UnixNano() >= s.downUntil.Load()
}

func (s *sharedFails) failed() {
	s.downUntil.Store(s.now().Add(sharedFailBackoff).UnixNano())
	if s.onFallback != nil {
		s.onFallback()
	}
}

// blocked returns how long until key may try again, according to the failures every replica
// recorded. ok is false when Redis could not answer (the caller uses its own count).
func (s *sharedFails) blocked(key string) (wait time.Duration, ok bool) {
	if !s.usable() {
		return 0, false
	}
	ctx, cancel := s.opContext()
	defer cancel()
	k := s.redisKey(key)
	now := s.now()
	cutoff := strconv.FormatInt(now.Add(-s.window).UnixNano(), 10)
	pipe := s.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, k, "-inf", "("+cutoff)
	card := pipe.ZCard(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		s.failed()
		return 0, false
	}
	n := int(card.Val())
	if n < s.max {
		return 0, true
	}
	// The failure that stops expiring the max-th most recent entry is the one that decides.
	res, err := s.rdb.ZRangeWithScores(ctx, k, int64(n-s.max), int64(n-s.max)).Result()
	if err != nil {
		s.failed()
		return 0, false
	}
	if len(res) == 0 {
		return 0, true
	}
	until := time.Unix(0, int64(res[0].Score)).Add(s.window)
	if w := until.Sub(now); w > 0 {
		return w, true
	}
	return 0, true
}

// fail records one failure; reached reports that this one brought the key to the limit.
func (s *sharedFails) fail(key string) (reached, ok bool) {
	if !s.usable() {
		return false, false
	}
	ctx, cancel := s.opContext()
	defer cancel()
	k := s.redisKey(key)
	now := s.now()
	var salt [6]byte
	_, _ = rand.Read(salt[:])
	pipe := s.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, k, "-inf", "("+strconv.FormatInt(now.Add(-s.window).UnixNano(), 10))
	pipe.ZAdd(ctx, k, redis.Z{Score: float64(now.UnixNano()), Member: strconv.FormatInt(now.UnixNano(), 10) + hex.EncodeToString(salt[:])})
	pipe.Expire(ctx, k, s.window+time.Minute)
	card := pipe.ZCard(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		s.failed()
		return false, false
	}
	return int(card.Val()) == s.max, true
}

// reset forgets key's failures (a right password).
func (s *sharedFails) reset(key string) {
	if !s.usable() {
		return
	}
	ctx, cancel := s.opContext()
	defer cancel()
	if err := s.rdb.Del(ctx, s.redisKey(key)).Err(); err != nil {
		s.failed()
	}
}
