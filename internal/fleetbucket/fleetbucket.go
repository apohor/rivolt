// Package fleetbucket paces the calls a pod makes to Rivian when it takes vehicles over (zero-impact deploys, G7). A roll moves every group
// within seconds; the 50 ms stagger spaces one process's first connects, but N pods and N vehicles together still arrive as a burst. Wait gives
// each call a slot: first from a per-process token bucket, then from a fleet-wide per-second budget in Redis, with active vehicles (driving or
// charging) kept ahead of idle ones. A wait is bounded, so pacing never becomes an outage: after maxWait the call goes ahead and is counted as
// starved. A Redis that cannot answer means only the local bucket applies.
//
// The fleet budget is a counter, not pub/sub: the app's Valkey user has no channel rights, and INCR plus EXPIRE is allowed.
package fleetbucket

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Config sizes a Bucket. Zero values fall back to the defaults below.
type Config struct {
	// LocalRPS and FleetRPS are the calls per second one process and the whole fleet may start. Defaults 10 and 20 (no Rivian 429 data yet: raise
	// them only from observed 429s and the takeover p95).
	LocalRPS int
	FleetRPS int
	// ActiveMaxWait and IdleMaxWait bound one Wait. Active vehicles must not wait long; idle ones may queue.
	ActiveMaxWait time.Duration
	IdleMaxWait   time.Duration
	// ShareTTL: calls with the same share key (one Rivian session's socket) within this long of a granted one go through without a slot.
	ShareTTL time.Duration
	// Redis and Prefix: the fleet budget. Nil Redis: local only.
	Redis  *redis.Client
	Prefix string
	// Now is the clock and Sleep the wait for the next second of the fleet budget (tests drive both, so nothing depends on how loaded the
	// machine is). Sleep returns when d has passed or ctx is done.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
	// Observe hears the outcome of every Wait (class and priority as the caller gave them): how long it waited, and whether it was shared,
	// starved or local-only. It is called once per Wait, on the caller's goroutine, and must not block.
	Observe func(class string, active bool, r Result)
}

const (
	defaultLocalRPS   = 10
	defaultFleetRPS   = 20
	defaultActiveWait = 30 * time.Second
	defaultIdleWait   = 5 * time.Minute
	defaultShareTTL   = 30 * time.Second
	redisOp           = 300 * time.Millisecond
	counterTTLSeconds = 5
	idleShareDivisor  = 2 // idle vehicles may use at most this fraction of the fleet budget (1/2), so active ones always find room
)

// Result says what a Wait did.
type Result struct {
	Waited  time.Duration
	Starved bool // the bound passed and the call went ahead without a slot
	Shared  bool // a call with the same share key was granted recently: no slot used
	Local   bool // Redis did not answer: the fleet budget was skipped
}

// Bucket is safe for concurrent use.
type Bucket struct {
	cfg   Config
	act   *rate.Limiter
	idle  *rate.Limiter
	mu    sync.Mutex
	share map[string]time.Time
}

// New builds a Bucket.
func New(cfg Config) *Bucket {
	if cfg.LocalRPS <= 0 {
		cfg.LocalRPS = defaultLocalRPS
	}
	if cfg.FleetRPS <= 0 {
		cfg.FleetRPS = defaultFleetRPS
	}
	if cfg.ActiveMaxWait <= 0 {
		cfg.ActiveMaxWait = defaultActiveWait
	}
	if cfg.IdleMaxWait <= 0 {
		cfg.IdleMaxWait = defaultIdleWait
	}
	if cfg.ShareTTL <= 0 {
		cfg.ShareTTL = defaultShareTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = realSleep
	}
	idleRPS := cfg.LocalRPS / idleShareDivisor
	if idleRPS < 1 {
		idleRPS = 1
	}
	return &Bucket{
		cfg:   cfg,
		act:   rate.NewLimiter(rate.Limit(cfg.LocalRPS), cfg.LocalRPS),
		idle:  rate.NewLimiter(rate.Limit(idleRPS), idleRPS),
		share: map[string]time.Time{},
	}
}

// Wait blocks until the call may start. class names the call (for the caller's metrics); shareKey, when not empty, lets the calls of one Rivian
// session's shared socket count once. It returns when ctx is done too (Waited then says how long it waited; the caller checks ctx).
func (b *Bucket) Wait(ctx context.Context, class, shareKey string, active bool) Result {
	r := b.wait(ctx, shareKey, active)
	if b != nil && b.cfg.Observe != nil {
		b.cfg.Observe(class, active, r)
	}
	return r
}

func (b *Bucket) wait(ctx context.Context, shareKey string, active bool) Result {
	if b == nil {
		return Result{}
	}
	start := b.cfg.Now()
	if shareKey != "" && b.sharedRecently(shareKey, start) {
		return Result{Shared: true}
	}
	maxWait := b.cfg.IdleMaxWait
	if active {
		maxWait = b.cfg.ActiveMaxWait
	}
	wctx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	res := Result{}
	lim := b.idle
	if active {
		lim = b.act
	}
	if err := lim.Wait(wctx); err != nil {
		res.Starved = ctx.Err() == nil
		res.Waited = b.cfg.Now().Sub(start)
		return res
	}
	if b.cfg.Redis != nil {
		ok, local := b.fleetSlot(wctx, active)
		res.Local = local
		res.Starved = !ok && ctx.Err() == nil
	}
	if shareKey != "" && !res.Starved {
		b.markShared(shareKey, b.cfg.Now())
	}
	res.Waited = b.cfg.Now().Sub(start)
	return res
}

func (b *Bucket) sharedRecently(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.share[key]
	return ok && now.Sub(at) < b.cfg.ShareTTL
}

func (b *Bucket) markShared(key string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.share[key] = now
	if len(b.share) > 4096 { // a bound, not a policy: forget the expired
		for k, at := range b.share {
			if now.Sub(at) >= b.cfg.ShareTTL {
				delete(b.share, k)
			}
		}
	}
}

func (b *Bucket) key(kind string, sec int64) string {
	return fmt.Sprintf("%s:rivian:takeover:%s:%d", b.cfg.Prefix, kind, sec)
}

// fleetSlot takes a slot from this second's fleet budget, trying the next second until it gets one or wctx ends. ok is false when the bound
// passed; local is true when Redis could not answer (the slot is then granted: fail open).
//
// The two kinds count their GRANTED calls in separate counters and are admitted by one atomic script (admitScript): together they never pass the
// budget, and idle calls never pass half of it, so an active call always finds room.
func (b *Bucket) fleetSlot(wctx context.Context, active bool) (ok, local bool) {
	for {
		sec := b.cfg.Now().Unix()
		kind := "i"
		if active {
			kind = "a"
		}
		granted, err := b.admit(wctx, sec, kind)
		if err != nil {
			return true, true
		}
		if granted {
			return true, false
		}
		// Over this second's budget: wait for the next second, with a little jitter so waiters do not all come back on the tick.
		next := time.Unix(sec+1, 0).Sub(b.cfg.Now()) + time.Duration(rand.Int64N(int64(250*time.Millisecond)))
		if next < 0 {
			next = 0
		}
		b.cfg.Sleep(wctx, next)
		if wctx.Err() != nil {
			return false, false
		}
	}
}

// realSleep waits d or until ctx is done.
func realSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// admitScript grants one call out of this second's budget, atomically (one Redis round trip, so concurrent pods cannot both take the last slot):
// KEYS[1] counts the active calls GRANTED this second, KEYS[2] the idle ones; ARGV[1] is "a" or "i", ARGV[2] the whole budget, ARGV[3] the idle share,
// ARGV[4] the counter's lifetime in seconds. An active call is granted while active + idle < budget; an idle call while idle < its share AND
// active + idle < budget. So the two together can never pass the budget, idle never passes its share (an active call always finds at least the
// other half), and a denied attempt leaves no trace in the counters. Returns 1 when granted.
var admitScript = redis.NewScript(`
local a = tonumber(redis.call('GET', KEYS[1]) or '0')
local i = tonumber(redis.call('GET', KEYS[2]) or '0')
local budget = tonumber(ARGV[2])
if a + i >= budget then return 0 end
if ARGV[1] == 'a' then
  redis.call('INCR', KEYS[1]); redis.call('EXPIRE', KEYS[1], ARGV[4]); return 1
end
if i >= tonumber(ARGV[3]) then return 0 end
redis.call('INCR', KEYS[2]); redis.call('EXPIRE', KEYS[2], ARGV[4]); return 1`)

// admit asks Redis for a slot in second sec for a call of the given kind ("a" or "i").
func (b *Bucket) admit(ctx context.Context, sec int64, kind string) (bool, error) {
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	idleShare := b.cfg.FleetRPS / idleShareDivisor
	if idleShare < 1 {
		idleShare = 1
	}
	n, err := admitScript.Run(c, b.cfg.Redis, []string{b.key("a", sec), b.key("i", sec)}, kind, b.cfg.FleetRPS, idleShare, counterTTLSeconds).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
