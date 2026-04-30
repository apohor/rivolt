// Package ratelimit implements the global upstream token bucket
// gating outbound calls to Rivian's GraphQL gateway.
//
// Why a token bucket and not a leaky bucket / fixed window:
//
//   - Bursty work is fine: a UI page that fires 4 parallel
//     queries should sail through the bucket if there's budget,
//     not get slow-walked by a leaky bucket.
//   - 429s come in waves; we want to absorb the wave with our
//     budget then refill, not run a tight rolling window that
//     punishes the recovery as much as the spike.
//
// Why Redis and not in-process: at N>1 pods, every pod
// independently classifying its own outbound rate produces N×
// the real rate against Rivian. A shared bucket forces the whole
// fleet to honour a single budget.
//
// Storage shape: one Redis hash per class (main + priority) with
// fields {tokens, ts}. The check-and-decrement runs as a Lua
// script so the whole "read → refill based on elapsed → maybe
// decrement → write" sequence is atomic with no client-side
// round trips.
//
// Two classes:
//
//   - "priority" — user-initiated calls that block the UI
//     (Login, RefreshToken, getUserInformation on first paint).
//     Smaller bucket, refilled separately so background work
//     can never starve a logged-in human.
//   - "main" — everything else (state polls, vehicle metadata,
//     subscriptions' REST seeds). Bigger bucket; absorbs
//     reconnect storms.
//
// Allow returns (allowed, retryAfter). When allowed is false,
// retryAfter is the time until at least one token will be
// available, suitable for an HTTP Retry-After header. Callers
// should respect this — we'll plumb the breaker / api 503 path
// to use it in the next change.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Class is the token-bucket selector. Stable on the wire — the
// strings appear in Redis keys and Prometheus labels.
type Class string

const (
	// ClassMain is the default for unattended, retry-friendly
	// upstream calls (state polls, REST seeds, metadata).
	ClassMain Class = "main"
	// ClassPriority is for user-blocking calls (Login,
	// RefreshToken). Reserved budget so a reconnect storm in
	// ClassMain can't lock new users out.
	ClassPriority Class = "priority"
)

// Config holds bucket parameters per class. Capacity is the burst
// allowance; RefillPerSec tunes steady-state throughput.
type Config struct {
	MainCapacity         int
	MainRefillPerSec     float64
	PriorityCapacity     int
	PriorityRefillPerSec float64

	// KeyPrefix lets multiple rivolt deployments share one Redis.
	// Defaults to "rivolt:rl".
	KeyPrefix string

	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// DefaultConfig returns sane defaults sized for a single-region
// homelab against Rivian's gateway. Tuned conservatively — the
// breaker still trips on bursts that exceed these.
func DefaultConfig() Config {
	return Config{
		MainCapacity:         60,
		MainRefillPerSec:     2, // 120/min steady state
		PriorityCapacity:     20,
		PriorityRefillPerSec: 1, // 60/min steady state
		KeyPrefix:            "rivolt:rl",
	}
}

// Limiter is the public type. Build with New, call Allow on every
// outbound Rivian call. Safe for concurrent use; all state lives
// in Redis.
type Limiter struct {
	rdb *redis.Client
	cfg Config
	log *slog.Logger

	script *redis.Script
}

// luaCheckAndDecrement is the atomic refill+take. KEYS[1] is the
// hash key for the class; ARGV is {capacity, refillPerSec, now_ms,
// requested_tokens}. Returns {allowed (0/1), retryAfter_ms,
// tokens_remaining (rounded down)}.
//
// Refill formula: elapsed_ms / 1000 * refillPerSec, capped at
// capacity. Then attempt to subtract requested; on insufficient
// budget compute retryAfter = (requested - tokens) / refillPerSec.
//
// Stored as floats so partial refill across sub-second windows
// doesn't get rounded to zero. Written as integer-cents-style
// scaled ints (×1000) inside the script to avoid Lua's float
// quirks; converted back at boundaries.
const luaCheckAndDecrement = `
local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local now_ms   = tonumber(ARGV[3])
local want     = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

if tokens == nil then tokens = capacity end
if ts == nil then ts = now_ms end

local elapsed_ms = now_ms - ts
if elapsed_ms < 0 then elapsed_ms = 0 end
local refilled = tokens + (elapsed_ms / 1000.0) * refill
if refilled > capacity then refilled = capacity end

local allowed = 0
local retry_ms = 0
if refilled >= want then
  refilled = refilled - want
  allowed = 1
else
  local short = want - refilled
  if refill > 0 then
    retry_ms = math.ceil((short / refill) * 1000)
  else
    retry_ms = 60000
  end
end

redis.call('HSET', key, 'tokens', refilled, 'ts', now_ms)
-- Expire 10× the refill-to-full time so idle keys vacate; keeps
-- one-off accounts from accumulating. Cap at 1h.
local ttl_ms = 3600000
if refill > 0 then
  ttl_ms = math.min(ttl_ms, math.ceil((capacity / refill) * 10000))
end
redis.call('PEXPIRE', key, ttl_ms)

return {allowed, retry_ms, math.floor(refilled)}
`

// New constructs a Limiter wrapping rdb. Pings Redis up to ctx's
// deadline and returns an error if the cluster is unreachable —
// callers can decide whether that's fatal at boot or whether to
// fall back to a no-op limiter.
func New(ctx context.Context, rdb *redis.Client, cfg Config, logger *slog.Logger) (*Limiter, error) {
	if rdb == nil {
		return nil, errors.New("ratelimit: nil redis client")
	}
	if logger == nil {
		logger = slog.Default()
	}
	d := DefaultConfig()
	if cfg.MainCapacity <= 0 {
		cfg.MainCapacity = d.MainCapacity
	}
	if cfg.MainRefillPerSec <= 0 {
		cfg.MainRefillPerSec = d.MainRefillPerSec
	}
	if cfg.PriorityCapacity <= 0 {
		cfg.PriorityCapacity = d.PriorityCapacity
	}
	if cfg.PriorityRefillPerSec <= 0 {
		cfg.PriorityRefillPerSec = d.PriorityRefillPerSec
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = d.KeyPrefix
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ratelimit: ping: %w", err)
	}
	return &Limiter{
		rdb:    rdb,
		cfg:    cfg,
		log:    logger,
		script: redis.NewScript(luaCheckAndDecrement),
	}, nil
}

// Allow attempts to take one token from the named class's bucket.
// Returns (true, 0) on success and (false, retryAfter) when out
// of budget. retryAfter is the time until at least one token
// would be available; suitable for HTTP Retry-After.
//
// On Redis errors Allow fail-opens (returns true) and logs at
// warn — a Redis blip should never black-hole the entire upstream
// path. The breaker still gates if Rivian responds with 429s.
func (l *Limiter) Allow(ctx context.Context, class Class) (bool, time.Duration) {
	cap, refill := l.params(class)
	now := l.cfg.Now().UnixMilli()
	res, err := l.script.Run(ctx, l.rdb, []string{l.key(class)},
		cap, refill, now, 1,
	).Slice()
	if err != nil {
		l.log.Warn("ratelimit: redis script failed; fail-open",
			"class", string(class), "err", err.Error())
		return true, 0
	}
	if len(res) < 2 {
		return true, 0
	}
	allowed, _ := toInt64(res[0])
	retryMs, _ := toInt64(res[1])
	if allowed == 1 {
		return true, 0
	}
	return false, time.Duration(retryMs) * time.Millisecond
}

// params returns (capacity, refillPerSec) for the given class.
// Unknown classes get the main settings — explicit fall-through
// is preferable to panicking on a typo at the call site.
func (l *Limiter) params(class Class) (int, float64) {
	switch class {
	case ClassPriority:
		return l.cfg.PriorityCapacity, l.cfg.PriorityRefillPerSec
	default:
		return l.cfg.MainCapacity, l.cfg.MainRefillPerSec
	}
}

func (l *Limiter) key(class Class) string {
	return l.cfg.KeyPrefix + ":" + string(class)
}

// toInt64 unpacks the Lua return values, which arrive as either
// int64 (most cases) or string (some redis client versions).
func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}
