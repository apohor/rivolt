package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// vehicleFallbackGate keeps the "linked account, no vehicle stored yet" Rivian lookup of /api/vehicles from running on every page load.
//
// That lookup is the one place the list still asks Rivian (single-owner M3-5a); it exists because the sign-in's prime may have failed, and
// the list is where that was retried. If Rivian keeps failing for the account, the web (which loads the list a lot) would turn every load,
// on every pod, into a Rivian call. So an attempt is claimed first: one attempt per user per TTL (5 minutes) across pods, whatever its result.
// The result is remembered with the claim ("error", "empty" or "stored") so a request inside the window can answer without Rivian.
//
// The claim is a Redis key under the install's key prefix, named by an HMAC of the user id (a user id never reaches Redis in clear), with a
// TTL. When Redis is not configured or cannot answer, the pod keeps its own claim in memory: at most one attempt per user per TTL per pod, so
// the bound is the number of pods, and the map is swept as it grows. It never fails open to "ask Rivian every time".
type vehicleFallbackGate struct {
	rdb     *redis.Client
	prefix  string
	key     []byte
	ttl     time.Duration
	now     func() time.Time
	observe func(outcome string)

	mu    sync.Mutex
	local map[string]vehicleFallbackEntry
	// downUntil (unix nanos): Redis is left alone until then after a failure.
	downUntil atomic.Int64
}

type vehicleFallbackEntry struct {
	until   time.Time
	outcome string // "pending" until the attempt reports
}

const (
	vehicleFallbackOpTimeout = 250 * time.Millisecond
	vehicleFallbackBackoff   = 15 * time.Second
)

// vehicleFallbackKey derives the gate's HMAC key from the limiters' key (a purpose-labelled subkey, as FailLimiterKey does from the server's
// secret), so one secret is not used raw for two purposes. nil stays nil: without a key the gate keeps its claims per pod.
func vehicleFallbackKey(limiterKey []byte) []byte {
	if len(limiterKey) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, limiterKey)
	m.Write([]byte("rivolt/vehicles-fallback/redis-keys/v1"))
	return m.Sum(nil)
}

func newVehicleFallbackGate(rdb *redis.Client, prefix string, key []byte, ttl time.Duration, observe func(string)) *vehicleFallbackGate {
	return &vehicleFallbackGate{rdb: rdb, prefix: prefix, key: vehicleFallbackKey(key), ttl: ttl, now: time.Now, observe: observe, local: map[string]vehicleFallbackEntry{}}
}

func (g *vehicleFallbackGate) redisKey(uid uuid.UUID) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte("vehicles-fallback|" + uid.String()))
	return g.prefix + ":vehfb:" + hex.EncodeToString(m.Sum(nil))
}

func (g *vehicleFallbackGate) redisUsable() bool {
	return g.rdb != nil && len(g.key) > 0 && g.now().UnixNano() >= g.downUntil.Load()
}

func (g *vehicleFallbackGate) redisFailed() {
	g.downUntil.Store(g.now().Add(vehicleFallbackBackoff).UnixNano())
}

// claim reports whether this request may ask Rivian. When it may not, prior is what the last attempt in the window ended in.
func (g *vehicleFallbackGate) claim(ctx context.Context, uid uuid.UUID) (ok bool, prior string) {
	k := uid.String()
	now := g.now()
	g.mu.Lock()
	if e, have := g.local[k]; have && now.Before(e.until) {
		g.mu.Unlock()
		return false, e.outcome
	}
	g.mu.Unlock()
	if g.redisUsable() {
		cctx, cancel := context.WithTimeout(ctx, vehicleFallbackOpTimeout)
		defer cancel()
		rk := g.redisKey(uid)
		set, err := g.rdb.SetNX(cctx, rk, "pending", g.ttl).Result()
		switch {
		case err != nil:
			g.redisFailed()
		case !set:
			val, gerr := g.rdb.Get(cctx, rk).Result()
			if gerr != nil {
				val = "error"
			}
			g.remember(k, val, g.ttl)
			return false, val
		}
	}
	g.remember(k, "pending", g.ttl)
	return true, ""
}

// done records how the attempt ended, for the requests inside the window.
func (g *vehicleFallbackGate) done(ctx context.Context, uid uuid.UUID, outcome string) {
	g.mu.Lock()
	if e, have := g.local[uid.String()]; have {
		e.outcome = outcome
		g.local[uid.String()] = e
	}
	g.mu.Unlock()
	if g.redisUsable() {
		cctx, cancel := context.WithTimeout(ctx, vehicleFallbackOpTimeout)
		defer cancel()
		if err := g.rdb.SetArgs(cctx, g.redisKey(uid), outcome, redis.SetArgs{Mode: "XX", KeepTTL: true}).Err(); err != nil && err != redis.Nil {
			g.redisFailed()
		}
	}
}

func (g *vehicleFallbackGate) remember(k, outcome string, ttl time.Duration) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.local) > 1024 {
		for kk, e := range g.local {
			if !now.Before(e.until) {
				delete(g.local, kk)
			}
		}
	}
	g.local[k] = vehicleFallbackEntry{until: now.Add(ttl), outcome: outcome}
}

// captureWriter lets the fallback see how the old list path ended without changing what it sends.
type captureWriter struct {
	http.ResponseWriter
	code int
	body bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if c.body.Len() < 64 {
		c.body.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// outcome is "error" for a failed answer, "empty" for an empty list, else "stored" (vehicles came back and were stored).
func (c *captureWriter) outcome() string {
	switch {
	case c.code >= 400:
		return "error"
	case c.body.String() == "[]\n":
		return "empty"
	}
	return "stored"
}
