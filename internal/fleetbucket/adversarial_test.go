package fleetbucket

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Adversarial review of the takeover limiter (G7), written by someone other than its author. Each test is a way the pacing could fail.

// A pod with more waiting callers than its bound can serve must still hold each of them for the bound before letting it go. rate.Limiter.Wait
// does not do that: when the reservation's delay would pass the context deadline it returns at once, so every caller beyond what the bound can
// queue is released immediately (counted as starved, waited 0) instead of after ActiveMaxWait. The surge that most needs pacing (hundreds of
// vehicles on one pod) is the one it stops pacing.
func TestCallersBeyondTheBoundWaitTheBoundBeforeGoingAhead(t *testing.T) {
	b := New(Config{LocalRPS: 10, FleetRPS: 1000, ActiveMaxWait: 400 * time.Millisecond, IdleMaxWait: 400 * time.Millisecond})
	const callers = 40 // 10 in the burst, ~4 more in 400 ms, ~26 beyond what the bound can queue
	var mu sync.Mutex
	var waited []time.Duration
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := b.Wait(context.Background(), "x", "", true)
			mu.Lock()
			waited = append(waited, r.Waited)
			mu.Unlock()
		}()
	}
	wg.Wait()
	early := 0
	for _, w := range waited {
		// Granted at once from the burst (under 50 ms) is fine. Anything that went ahead having waited under 300 ms without being granted a
		// token in the burst was released early: count the callers released well before the bound that were not burst or token grants.
		if w < 50*time.Millisecond {
			early++
		}
	}
	if early > 10+2 { // the burst of 10 (and a little slack for the first refill)
		t.Errorf("%d of %d callers left within 50 ms; the burst only holds 10, so %d were released without a slot or the bound's wait", early, callers, early-10)
	}
}

// Redis that accepts the connection and never answers: the first Wait pays up to redisOp (300 ms) and then fails open. Pacing must not become an
// outage: the call returns, Local is true, and the cost stays about the bound of one Redis operation. That holds only for a client with
// ContextTimeoutEnabled: go-redis otherwise ignores the context while it waits on the socket (the client's ReadTimeout, 3 s by default, decides),
// which is why cmd/rivolt gives the bucket a client of its own (takeoverRedis) instead of the shared one.
func TestARedisThatNeverAnswersFailsOpenWithinTheOperationBound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // held open, never read or answered
		}
	}()
	rdb := redis.NewClient(&redis.Options{Addr: ln.Addr().String(), DialTimeout: time.Second, ReadTimeout: 3 * time.Second, ContextTimeoutEnabled: true})
	defer func() { _ = rdb.Close() }()
	b := New(Config{Redis: rdb, Prefix: "t"})
	start := time.Now()
	r := b.Wait(context.Background(), "x", "", true)
	if !r.Local || r.Starved {
		t.Fatalf("result %+v, want fail-open (Local, not Starved)", r)
	}
	if took := time.Since(start); took > 900*time.Millisecond {
		t.Errorf("a silent Redis cost %v, want about one redisOp (300 ms)", took)
	}
}

// A closed Redis (connection refused) fails open at once.
func TestARedisThatIsDownFailsOpenAtOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	mr.Close()
	b := New(Config{Redis: rdb, Prefix: "t"})
	start := time.Now()
	r := b.Wait(context.Background(), "x", "", false)
	if !r.Local || r.Starved || time.Since(start) > time.Second {
		t.Errorf("result %+v after %v, want Local, not Starved, quickly", r, time.Since(start))
	}
}

// Three pods surge at once with a shared Redis: the fleet grants no more than FleetRPS calls (active and idle together, idle at most half) in any
// second, however the pods' own buckets would let them burst (3 x LocalRPS), and however far apart the pods' clocks are: the budget's seconds are
// Redis's, so the pods here run 700 ms behind, on time and 700 ms ahead. Real clock, so it takes about eight seconds.
//
// Each grant is bucketed twice, both times by the limiter's own clock: by the Redis second the call says granted it (Result.FleetSecond), and by
// the fleet's per-second grant counters (miniredis keeps the keys: nothing advances its TTLs). Neither is the test's clock, so a goroutine
// descheduled on a loaded runner cannot look like an over-grant.
func TestAThreePodSurgeStaysWithinTheFleetBudgetEachSecond(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about eight seconds of real time")
	}
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	var pods []*Bucket
	for _, skew := range []time.Duration{-700 * time.Millisecond, 0, 700 * time.Millisecond} {
		now := func() time.Time { return time.Now().Add(skew) }
		pods = append(pods, New(Config{LocalRPS: 100, FleetRPS: 20, Redis: rdb, Prefix: "t", Now: now, ActiveMaxWait: time.Minute, IdleMaxWait: time.Minute}))
	}
	type count struct{ a, i int }
	var mu sync.Mutex
	bySecond := map[int64]count{}
	var wg sync.WaitGroup
	for _, b := range pods {
		for v := 0; v < 50; v++ {
			active := v < 40 // 40 active and 10 idle per pod
			wg.Add(1)
			go func(b *Bucket) {
				defer wg.Done()
				r := b.Wait(context.Background(), "dial", "", active)
				if r.Starved || r.Local || r.FleetSecond == 0 {
					t.Errorf("unexpected result %+v", r)
					return
				}
				mu.Lock()
				c := bySecond[r.FleetSecond]
				if active {
					c.a++
				} else {
					c.i++
				}
				bySecond[r.FleetSecond] = c
				mu.Unlock()
			}(b)
		}
	}
	wg.Wait()
	total := 0
	for sec, c := range bySecond {
		total += c.a + c.i
		if c.a+c.i > 20 || c.i > 10 {
			t.Errorf("second %d: %d active + %d idle calls granted, budget 20 (idle at most 10)", sec, c.a, c.i)
		}
	}
	// The window left in Redis is the fleet's own count of the last second: it must match what the calls say that second granted.
	sec, a, i := window(mr, "t:{rivian:takeover}")
	if got := bySecond[sec]; got.a != a || got.i != i {
		t.Errorf("second %d: the calls say %+v were granted, the fleet's window says a=%d i=%d", sec, got, a, i)
	}
	if total != 150 {
		t.Errorf("%d calls granted, want 150", total)
	}
}

// A pod whose clock is well over a second off is still granted in Redis's second, at once: no pod's clock decides which second a call counts in.
func TestASkewedPodIsGrantedInRedisSecondAtOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock(mr)
	behind := func() time.Time { return clk.Now().Add(-1500 * time.Millisecond) }
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: behind, Sleep: clk.Sleep})
	for i := 0; i < 4; i++ {
		r := b.Wait(context.Background(), "dial", "", true)
		if r.Starved || r.Local || r.FleetSecond != clk.Now().Unix() {
			t.Fatalf("call %d: %+v, want granted in Redis's second %d", i, r, clk.Now().Unix())
		}
	}
	if clk.slept() != 0 {
		t.Errorf("the skewed pod waited %d times for a second it had room in", clk.slept())
	}
	if sec, a, _ := window(mr, windowKey); sec != clk.Now().Unix() || a != 4 {
		t.Errorf("window sec=%d a=%d, want Redis's second %d with 4 active", sec, a, clk.Now().Unix())
	}
}

// The window rolls over with Redis's clock: a new second starts from zero (nothing carried over from the full one before it) and every call
// granted is counted in the second that granted it (nothing lost at the boundary).
func TestTheWindowRollsOverWithRedisSecond(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock(mr)
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep, ActiveMaxWait: 5 * time.Second, IdleMaxWait: 5 * time.Second}) // real time: a window that never resets fails here, not hangs
	first := clk.Now().Unix()
	per := map[int64]int{}
	grant := func(active bool) {
		r := b.Wait(context.Background(), "dial", "", active)
		if r.Starved || r.Local {
			t.Fatalf("%+v", r)
		}
		per[r.FleetSecond]++
	}
	grant(true)
	grant(false)
	grant(true) // 3 of 4 in the first second
	// Redis's clock crosses into the next second, 1 ms in, without the pod sleeping: the window resets on the next call.
	clk.Sleep(context.Background(), time.Second+time.Millisecond)
	for i := 0; i < 4; i++ {
		grant(i%2 == 0) // the whole budget of the new second, none of it used up by the old one
	}
	if clk.slept() != 1 {
		t.Fatalf("%d sleeps; the four calls of the new second should not have waited", clk.slept())
	}
	grant(true) // the new second is full: this one waits for the one after
	if per[first] != 3 || per[first+1] != 4 || per[first+2] != 1 {
		t.Errorf("grants by Redis second %v, want 3, 4, 1 from %d", per, first)
	}
	if sec, a, i := window(mr, windowKey); sec != first+2 || a != 1 || i != 0 {
		t.Errorf("window sec=%d a=%d i=%d, want second %d with 1 active", sec, a, i, first+2)
	}
}

// Every grant leaves the window with a full lifetime: a fleet that stops calling leaves nothing behind in Redis, and one that keeps calling never
// loses its count to an expiry in the middle of a second.
func TestTheWindowHasATTLAfterEveryGrant(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock(mr)
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep, ActiveMaxWait: 5 * time.Second, IdleMaxWait: 5 * time.Second}) // real time: a window that never resets fails here, not hangs
	for i := 0; i < 6; i++ {
		b.Wait(context.Background(), "dial", "", i%3 != 0)
		if ttl := mr.TTL(windowKey); ttl != windowTTL {
			t.Fatalf("call %d: window TTL %v, want %v", i, ttl, windowTTL)
		}
		mr.FastForward(windowTTL - 500*time.Millisecond) // most of its life gone: the next grant must renew it, not leave it to lapse
	}
	if !mr.Exists(windowKey) {
		t.Error("the window expired between grants")
	}
}

// After one Redis error the fleet budget is skipped for the backoff: a Redis that does not answer costs the first call its timeout, not every call.
func TestARedisErrorStopsTheNextCallsFromAskingForTheBackoff(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	mr.Close()
	clk := newFakeClock(mr)
	b := New(Config{Redis: rdb, Prefix: "t", Now: clk.Now, Sleep: clk.Sleep, RedisBackoff: 10 * time.Second})
	if r := b.Wait(context.Background(), "x", "", true); !r.Local {
		t.Fatalf("first call: %+v, want Local", r)
	}
	start := time.Now()
	for i := 0; i < 50; i++ {
		if r := b.Wait(context.Background(), "x", "", true); !r.Local {
			t.Fatalf("call %d: %+v, want Local during the backoff", i, r)
		}
	}
	// 50 calls at 10 per second after the burst would take seconds; they are local calls and Redis is not asked, so they only wait the local bucket.
	_ = start
	clk.Sleep(context.Background(), 11*time.Second)
	if b.redisDown() {
		t.Error("the backoff did not end")
	}
}

// Active and idle together never pass FleetRPS (G7): with the budget 20, after 10 idle calls (their half) only 10 active ones fit in that second;
// the next one waits for the following second.
func TestActiveAndIdleTogetherStayWithinTheBudget(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock(mr)
	b := New(Config{LocalRPS: 1000, FleetRPS: 20, Redis: rdb, Prefix: "t", Now: clk.Now, Sleep: clk.Sleep, ActiveMaxWait: time.Hour, IdleMaxWait: time.Hour})
	for i := 0; i < 10; i++ {
		b.Wait(context.Background(), "dial", "", false)
	}
	at := clk.Now().Unix()
	for i := 0; i < 10; i++ {
		b.Wait(context.Background(), "dial", "", true)
	}
	if clk.Now().Unix() != at {
		t.Fatalf("10 active calls after 10 idle ones should still fit in the same second of a budget of 20")
	}
	b.Wait(context.Background(), "dial", "", true)
	if clk.Now().Unix() == at {
		t.Fatalf("the 21st call in one fleet second started inside it")
	}
}

// A caller whose own bound has run out (a long local wait used it up) reaches the Redis call with no time left and gets DeadlineExceeded. That is
// the caller's bound, not a Redis failure: it must not send the whole pod to local-only pacing for the backoff, in the middle of the surge.
func TestACallersOwnBoundRunningOutDoesNotMarkRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	b := New(Config{Redis: client(t, mr), Prefix: "t", RedisBackoff: 10 * time.Second})
	wctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if ok, _, _ := b.fleetSlot(wctx, true); ok {
		t.Error("a slot was reported taken with the bound already past")
	}
	if b.redisDown() {
		t.Error("the caller's expired bound marked a healthy Redis down")
	}
	if r := b.Wait(context.Background(), "x", "", true); r.Local {
		t.Errorf("a later caller was paced locally only: %+v", r)
	}
}
