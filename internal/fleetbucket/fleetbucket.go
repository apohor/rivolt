// Package fleetbucket paces the calls a pod makes to Rivian when it takes vehicles over (zero-impact deploys, G7). A roll moves every group
// within seconds; the 50 ms stagger spaces one process's first connects, but N pods and N vehicles together still arrive as a burst. Wait gives
// each call a slot: first from a per-process token bucket, then from a fleet-wide per-second budget in Redis, with active vehicles (driving or
// charging) kept ahead of idle ones. A wait is bounded, so pacing never becomes an outage: after maxWait the call goes ahead and is counted as
// starved. A Redis that cannot answer means only the local bucket applies.
//
// The fleet budget is a counter, not pub/sub: the app's Valkey user has no channel rights, and scripts plus TIME are allowed. Its seconds are
// Redis's (TIME, read inside the admission script), not any pod's: pods whose clocks disagree still count each call in the same second.
package fleetbucket

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
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
	// budget's seconds are Redis's own (Now only guesses which second to name).
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
	counterTTLSeconds = 5
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
	// offset is Redis's clock minus Now, in nanoseconds, as last seen: it makes the guess of Redis's current second right first time.
	offset atomic.Int64
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

// key names one second's counter. The {rivian:takeover} hash tag puts every takeover key in one Redis Cluster slot, as the admission script
// needs (it reads both counters of a second); it comes after the prefix so the app user's ACL key pattern (~<prefix>:*) still matches.
func (b *Bucket) key(kind string, sec int64) string {
	return fmt.Sprintf("%s:{rivian:takeover}:%s:%d", b.cfg.Prefix, kind, sec)
}

// fleetSlot takes a slot from the fleet budget of Redis's current second, trying the next second until it gets one or wctx ends. ok is false when
// the bound passed; local is true when Redis could not answer (the slot is then granted: fail open); sec is the Redis second that granted it.
//
// The two kinds count their GRANTED calls in separate counters and are admitted by one atomic script (admitScript): together they never pass the
// budget, and idle calls never pass half of it, so an active call always finds room.
func (b *Bucket) fleetSlot(wctx context.Context, active bool) (ok, local bool, sec int64) {
	kind := "i"
	if active {
		kind = "a"
	}
	for {
		guess := b.cfg.Now().Add(time.Duration(b.offset.Load())).Unix()
		st, rsec, rusec, err := b.admit(wctx, guess, kind)
		if err != nil {
			if wctx.Err() != nil {
				// The caller's own bound ran out before or during the call (a long local wait left it little time): Redis did not fail, so do not
				// send the whole pod to local-only pacing for the backoff, and do not claim a slot that was never taken.
				return false, false, 0
			}
			b.markRedisDown()
			return true, true, 0
		}
		redisNow := time.Unix(rsec, rusec*int64(time.Microsecond))
		b.offset.Store(int64(redisNow.Sub(b.cfg.Now())))
		switch st {
		case admitGranted:
			return true, false, rsec
		case admitWrongSecond:
			// This pod's clock named another second than Redis's: nothing was counted. Ask again for Redis's second (the offset now knows it).
			if wctx.Err() != nil {
				return false, false, 0
			}
			continue
		}
		// Over this second's budget: wait for Redis's next second, with a little jitter so waiters do not all come back on the tick.
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

const (
	admitDenied      = 0
	admitGranted     = 1
	admitWrongSecond = -1
)

// admitScript grants one call out of the budget of Redis's current second, atomically (one round trip, so concurrent pods cannot both take the
// last slot). The second is Redis's TIME, not the caller's clock: ARGV[1] is the second the caller guessed and named KEYS by, and a wrong guess
// is answered -1 with nothing counted. KEYS[1] counts the active calls GRANTED in that second, KEYS[2] the idle ones; ARGV[2] is "a" or "i",
// ARGV[3] the whole budget, ARGV[4] the idle share, ARGV[5] the counters' lifetime in seconds. An active call is granted while active + idle <
// budget; an idle call while idle < its share AND active + idle < budget. So the two together never pass the budget, idle never passes its share
// (an active call always finds at least the other half), and a denied attempt leaves no trace. Returns {status, second, microseconds}: status 1
// granted, 0 denied, -1 wrong second; second and microseconds are Redis's TIME.
var admitScript = redis.NewScript(`
pcall(redis.replicate_commands)
local t = redis.call('TIME')
local sec = tonumber(t[1])
local usec = tonumber(t[2])
if sec ~= tonumber(ARGV[1]) then return {-1, sec, usec} end
local a = tonumber(redis.call('GET', KEYS[1]) or '0')
local i = tonumber(redis.call('GET', KEYS[2]) or '0')
if a + i >= tonumber(ARGV[3]) then return {0, sec, usec} end
if ARGV[2] == 'a' then
  redis.call('INCR', KEYS[1]); redis.call('EXPIRE', KEYS[1], ARGV[5]); return {1, sec, usec}
end
if i >= tonumber(ARGV[4]) then return {0, sec, usec} end
redis.call('INCR', KEYS[2]); redis.call('EXPIRE', KEYS[2], ARGV[5]); return {1, sec, usec}`)

// admit asks Redis for a slot of the given kind ("a" or "i") in second sec (the caller's guess of Redis's current second).
func (b *Bucket) admit(ctx context.Context, sec int64, kind string) (status int, rsec, rusec int64, err error) {
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	idleShare := b.cfg.FleetRPS / idleShareDivisor
	if idleShare < 1 {
		idleShare = 1
	}
	v, err := admitScript.Run(c, b.cfg.Redis, []string{b.key("a", sec), b.key("i", sec)}, sec, kind, b.cfg.FleetRPS, idleShare, counterTTLSeconds).Int64Slice()
	if err != nil {
		return 0, 0, 0, err
	}
	if len(v) != 3 {
		return 0, 0, 0, fmt.Errorf("fleetbucket: admit script returned %d values", len(v))
	}
	return int(v[0]), v[1], v[2], nil
}
