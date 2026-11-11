// Package fleetbucket paces the calls a pod makes to Rivian when it takes vehicles over (zero-impact deploys, G7). A roll moves every group
// within seconds; the 50 ms stagger spaces one process's first connects, but N pods and N vehicles together still arrive as a burst. Wait gives
// each call a slot: first from a per-process token bucket, then from a fleet-wide per-second budget in Redis, with active vehicles (driving or
// charging) kept ahead of idle ones. A wait is bounded, so pacing never becomes an outage: after maxWait the call goes ahead and is counted as
// starved. A Redis that cannot answer means only the local bucket applies.
//
// The fleet budget is a counter, not pub/sub: the app's Valkey user has no channel rights, and scripts plus TIME are allowed. Its seconds are
// Redis's (TIME, read inside the admission script), not any pod's: pods whose clocks disagree still count each call in the same second, and no
// pod's clock names a key.
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
	// them only from observed 429s and the takeover p95). FleetRPS is the TOTAL for active and idle calls together; idle calls may use at most
	// half of it, so active ones always find the other half.
	LocalRPS int
	FleetRPS int
	// ActiveMaxWait and IdleMaxWait bound one Wait. Active vehicles must not wait long; idle ones may queue.
	ActiveMaxWait time.Duration
	IdleMaxWait   time.Duration
	// ShareTTL: calls with the same share key (one Rivian session's socket) within this long of a granted one go through without a slot.
	ShareTTL time.Duration
	// RedisBackoff: after a Redis error the fleet budget is skipped (local only) for this long, so a Redis that does not answer costs one slow call, not every one.
	// Default 10 s.
	RedisBackoff time.Duration
	// Redis and Prefix: the fleet budget. Nil Redis: local only.
	Redis  *redis.Client
	Prefix string
	// Now is the clock and Sleep the wait for the next second of the fleet budget (tests drive both, so nothing depends on how loaded the
	// machine is). Sleep returns when d has passed or ctx is done. Now paces the local bucket, the share window and the Redis backoff; the fleet
	// budget's seconds are Redis's own.
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
	defaultBackoff    = 10 * time.Second
	redisOp           = 300 * time.Millisecond
	windowTTL         = 5 * time.Second
	idleShareDivisor  = 2 // idle vehicles may use at most this fraction of the fleet budget (1/2), so active ones always find room
)

// Result says what a Wait did.
type Result struct {
	Waited  time.Duration
	Starved bool // the bound passed and the call went ahead without a slot
	Shared  bool // a call with the same share key was granted recently: no slot used
	Local   bool // Redis did not answer: the fleet budget was skipped
	// FleetSecond is the Unix second, by Redis's clock, whose fleet budget granted the call (0: no fleet slot was granted).
	FleetSecond int64
}

// Bucket is safe for concurrent use.
type Bucket struct {
	cfg   Config
	act   *rate.Limiter
	idle  *rate.Limiter
	mu    sync.Mutex
	share map[string]time.Time
	// redisDownUntil: while the clock is before it, the fleet budget is skipped (a Redis error set it).
	redisDownUntil time.Time
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
	if cfg.RedisBackoff <= 0 {
		cfg.RedisBackoff = defaultBackoff
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
	// Reserve, not Wait: rate.Limiter.Wait returns at once when the delay would pass the context's deadline, which released every caller beyond
	// what the bound can queue immediately (the surge that most needs pacing was the one that was not paced). Here such a caller holds for the whole
	// bound and then goes ahead, counted as starved, as the package comment says.
	resv := lim.Reserve()
	if d := resv.Delay(); d > 0 {
		if d > maxWait {
			resv.Cancel()
			<-wctx.Done()
			res.Starved = ctx.Err() == nil
			res.Waited = b.cfg.Now().Sub(start)
			return res
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-wctx.Done():
			t.Stop()
			resv.Cancel()
			res.Starved = ctx.Err() == nil
			res.Waited = b.cfg.Now().Sub(start)
			return res
		}
	}
	if b.cfg.Redis != nil {
		if b.redisDown() {
			res.Local = true // a recent Redis error: only the local bucket applies for the backoff
		} else {
			ok, local, sec := b.fleetSlot(wctx, active)
			res.Local = local
			res.FleetSecond = sec
			res.Starved = !ok && ctx.Err() == nil
		}
	}
	if shareKey != "" && !res.Starved {
		b.markShared(shareKey, b.cfg.Now())
	}
	res.Waited = b.cfg.Now().Sub(start)
	return res
}

func (b *Bucket) redisDown() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg.Now().Before(b.redisDownUntil)
}

func (b *Bucket) markRedisDown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.redisDownUntil = b.cfg.Now().Add(b.cfg.RedisBackoff)
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

// key names the fleet budget's window: one hash holding Redis's current second and the calls granted in it. One key is one Redis Cluster slot
// whatever the second; the {rivian:takeover} hash tag keeps it in the same slot as any takeover key added later. The tag comes after the prefix
// so the app user's ACL key pattern (~<prefix>:*) still matches.
func (b *Bucket) key() string {
	return b.cfg.Prefix + ":{rivian:takeover}"
}

// fleetSlot takes a slot from the fleet budget of Redis's current second, trying the next second until it gets one or wctx ends. ok is false when
// the bound passed; local is true when Redis could not answer (the slot is then granted: fail open); sec is the Redis second that granted it.
//
// Active and idle calls count their GRANTED calls in one window and are admitted by one atomic script (admitScript): together they never pass the
// budget, and idle calls never pass half of it, so an active call always finds room.
func (b *Bucket) fleetSlot(wctx context.Context, active bool) (ok, local bool, sec int64) {
	kind := "i"
	if active {
		kind = "a"
	}
	for {
		granted, rsec, rusec, err := b.admit(wctx, kind)
		if err != nil {
			if wctx.Err() != nil {
				// The caller's own bound ran out before or during the call (a long local wait left it little time): Redis did not fail, so do not
				// send the whole pod to local-only pacing for the backoff, and do not claim a slot that was never taken.
				return false, false, 0
			}
			b.markRedisDown()
			return true, true, 0
		}
		if granted {
			return true, false, rsec
		}
		// Over this second's budget: wait for Redis's next second (by the microseconds Redis returned, not this pod's clock), with a little jitter
		// so waiters do not all come back on the tick.
		next := time.Second - time.Duration(rusec)*time.Microsecond + time.Duration(rand.Int64N(int64(250*time.Millisecond)))
		b.cfg.Sleep(wctx, next)
		if wctx.Err() != nil {
			return false, false, 0
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

// admitScript grants one call out of the budget of Redis's current second, atomically (one round trip, so concurrent pods cannot both take the
// last slot, and the window's reset cannot race). KEYS[1] is the window, a hash: sec (the Redis second it counts), a and i (the active and idle
// calls GRANTED in it). A window for an older second is reset first. ARGV[1] is "a" or "i", ARGV[2] the whole budget, ARGV[3] the idle share,
// ARGV[4] the window's lifetime in milliseconds. An active call is granted while a + i < budget; an idle call while i < its share AND a + i <
// budget. So the two together never pass the budget, idle never passes its share (an active call always finds at least the other half), and a
// denied attempt leaves no trace. Returns {granted (1 or 0), second, microseconds}, the last two Redis's TIME.
var admitScript = redis.NewScript(`
pcall(redis.replicate_commands)
local t = redis.call('TIME')
local now = tonumber(t[1])
local usec = tonumber(t[2])
local w = redis.call('HMGET', KEYS[1], 'sec', 'a', 'i')
local a, i = 0, 0
if tonumber(w[1]) == now then
  a = tonumber(w[2]) or 0
  i = tonumber(w[3]) or 0
else
  redis.call('HSET', KEYS[1], 'sec', now, 'a', 0, 'i', 0)
end
if a + i >= tonumber(ARGV[2]) then return {0, now, usec} end
if ARGV[1] == 'i' and i >= tonumber(ARGV[3]) then return {0, now, usec} end
redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return {1, now, usec}`)

// admit asks Redis for a slot of the given kind ("a" or "i") in its current second.
func (b *Bucket) admit(ctx context.Context, kind string) (granted bool, sec, usec int64, err error) {
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	idleShare := b.cfg.FleetRPS / idleShareDivisor
	if idleShare < 1 {
		idleShare = 1
	}
	v, err := admitScript.Run(c, b.cfg.Redis, []string{b.key()}, kind, b.cfg.FleetRPS, idleShare, windowTTL.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, 0, err
	}
	if len(v) != 3 {
		return false, 0, 0, fmt.Errorf("fleetbucket: admit script returned %d values", len(v))
	}
	return v[0] == 1, v[1], v[2], nil
}
