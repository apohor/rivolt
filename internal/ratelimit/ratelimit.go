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
	"math"
	"strconv"
	"sync"
	"sync/atomic"
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

	// FallbackDivisor sizes the per-process bucket used while Redis cannot answer: the
	// configured capacity and refill divided by it (zero or less: 3, the number of
	// replicas we run). With up to that many replicas the outage budget stays within
	// the configured one; it is never "no limit".
	FallbackDivisor float64
	// OnFallback, when set, is called for each call served from the per-process bucket.
	OnFallback func(class Class)
}

// withDefaults fills the zero values of cfg.
func withDefaults(cfg Config) Config {
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
	if cfg.FallbackDivisor <= 0 {
		cfg.FallbackDivisor = 3
	}
	return cfg
}

// bucket is a token bucket in this process.
type bucket struct {
	tokens float64
	last   time.Time
}

// fallbackBuckets is the per-process stand-in for the shared buckets while Redis cannot
// answer: the configured capacity and refill divided by cfg.FallbackDivisor (at least one
// token of capacity, so a class is never starved outright).
type fallbackBuckets struct {
	mu sync.Mutex
	b  map[Class]*bucket
}

func (f *fallbackBuckets) allow(class Class, cfg Config) (bool, time.Duration) {
	cap, refill := cfg.MainCapacity, cfg.MainRefillPerSec
	if class == ClassPriority {
		cap, refill = cfg.PriorityCapacity, cfg.PriorityRefillPerSec
	}
	capF := math.Max(1, float64(cap)/cfg.FallbackDivisor)
	refill /= cfg.FallbackDivisor
	if cfg.OnFallback != nil {
		cfg.OnFallback(class)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.b == nil {
		f.b = map[Class]*bucket{}
	}
	now := cfg.Now()
	bk, ok := f.b[class]
	if !ok {
		bk = &bucket{tokens: capF, last: now}
		f.b[class] = bk
	}
	if el := now.Sub(bk.last).Seconds(); el > 0 {
		bk.tokens = math.Min(capF, bk.tokens+el*refill)
		bk.last = now
	}
	if bk.tokens >= 1 {
		bk.tokens--
		return true, 0
	}
	if refill <= 0 {
		return false, time.Minute
	}
	return false, time.Duration((1 - bk.tokens) / refill * float64(time.Second))
}

// DefaultConfig returns sane defaults sized for a single-region
// deployment against Rivian's gateway. Tuned conservatively — the
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

	script   *redis.Script
	fallback fallbackBuckets
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
	cfg = withDefaults(cfg)
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
// On Redis errors Allow does not fail open: it draws from a per-process
// bucket sized as a share of the global one (Config.FallbackDivisor) and logs
// at warn. A Redis blip then costs the shared budget its accuracy, not its
// existence: the upstream path is not black-holed, and not unlimited either.
func (l *Limiter) Allow(ctx context.Context, class Class) (bool, time.Duration) {
	cap, refill := l.params(class)
	now := l.cfg.Now().UnixMilli()
	res, err := l.script.Run(ctx, l.rdb, []string{l.key(class)},
		cap, refill, now, 1,
	).Slice()
	if err != nil {
		l.log.Warn("ratelimit: redis script failed; using the per-process bucket",
			"class", string(class), "err", err.Error())
		return l.fallback.allow(class, l.cfg)
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

// Retrying is a Limiter that may not exist yet. Redis can be unreachable when
// the process boots (a rollout that starts Redis and the app together); the
// limiter used to be disabled for the life of the process in that case, so a
// later rollout fired Rivian calls with no global budget at all. Retrying keeps
// trying in the background and starts limiting as soon as Redis answers. Until
// then, like a Redis error in Allow, it uses the per-process bucket.
type Retrying struct {
	l        atomic.Pointer[Limiter]
	cfg      Config
	fallback fallbackBuckets
}

// Allow delegates to the Limiter once it exists; before that it draws from the
// per-process bucket (see Config.FallbackDivisor), so a boot without Redis is
// limited, not unlimited.
func (r *Retrying) Allow(ctx context.Context, class Class) (bool, time.Duration) {
	if l := r.l.Load(); l != nil {
		return l.Allow(ctx, class)
	}
	return r.fallback.allow(class, r.cfg)
}

// Ready reports whether the Redis connection has been made.
func (r *Retrying) Ready() bool { return r.l.Load() != nil }

// NewRetrying tries New once (bounded by pingTimeout) and, if that fails, keeps
// trying every retryEvery in the background until it works or ctx is done.
// onReady, when set, is called once when the connection is made later.
func NewRetrying(ctx context.Context, rdb *redis.Client, cfg Config, logger *slog.Logger, pingTimeout, retryEvery time.Duration, onReady func()) *Retrying {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Retrying{cfg: withDefaults(cfg)}
	try := func() error {
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		defer cancel()
		l, err := New(pctx, rdb, cfg, logger)
		if err != nil {
			return err
		}
		r.l.Store(l)
		return nil
	}
	err := try()
	if err == nil {
		return r
	}
	logger.Warn("ratelimit: redis unreachable; running without the global budget and retrying", "every", retryEvery.String(), "err", err.Error())
	go func() {
		t := time.NewTicker(retryEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := try(); err != nil {
					continue
				}
				logger.Info("ratelimit: redis reachable; global budget enabled")
				if onReady != nil {
					onReady()
				}
				return
			}
		}
	}()
	return r
}
