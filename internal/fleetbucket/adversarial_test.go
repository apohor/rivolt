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

// Three pods surge at once with a shared Redis: the fleet starts no more than FleetRPS active calls in any wall-clock second, however the pods' own
// buckets would let them burst (3 x LocalRPS). Real clock (the budget's seconds are Unix seconds), so it takes about six seconds.
func TestAThreePodSurgeStaysWithinTheFleetBudgetEachSecond(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about six seconds of real time")
	}
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	var mu sync.Mutex
	grants := map[int64]int{}
	var pods []*Bucket
	for i := 0; i < 3; i++ {
		pods = append(pods, New(Config{LocalRPS: 100, FleetRPS: 20, Redis: rdb, Prefix: "t", ActiveMaxWait: time.Minute}))
	}
	var wg sync.WaitGroup
	for _, b := range pods {
		for v := 0; v < 40; v++ {
			wg.Add(1)
			go func(b *Bucket) {
				defer wg.Done()
				r := b.Wait(context.Background(), "dial", "", true)
				if r.Starved || r.Local {
					t.Errorf("unexpected result %+v", r)
				}
				mu.Lock()
				grants[time.Now().Unix()]++
				mu.Unlock()
			}(b)
		}
	}
	wg.Wait()
	total := 0
	for sec, n := range grants {
		total += n
		if n > 20+4 { // a few milliseconds of slack at a second boundary: a grant is recorded just after it is made
			t.Errorf("second %d: %d active calls started, budget 20", sec, n)
		}
	}
	if total != 120 {
		t.Errorf("%d calls granted, want 120", total)
	}
}

// After one Redis error the fleet budget is skipped for the backoff: a Redis that does not answer costs the first call its timeout, not every call.
func TestARedisErrorStopsTheNextCallsFromAskingForTheBackoff(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	mr.Close()
	clk := newFakeClock()
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

// Idle calls never take the active calls' room, but active and idle together can exceed FleetRPS: an active call does not read the idle counter.
// This pins the arithmetic so a change is deliberate: with the budget 20, at most 20 active and 10 idle start in one second (30), not 20.
func TestActiveAndIdleTogetherCanReachOneAndAHalfTimesTheBudget(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 20, Redis: rdb, Prefix: "t", Now: clk.Now, Sleep: clk.Sleep, ActiveMaxWait: time.Hour, IdleMaxWait: time.Hour})
	// Idle first: they fill their half (10) and see 0 active.
	for i := 0; i < 10; i++ {
		b.Wait(context.Background(), "dial", "", false)
	}
	at := clk.Now().Unix()
	for i := 0; i < 20; i++ {
		b.Wait(context.Background(), "dial", "", true)
	}
	if clk.Now().Unix() != at {
		t.Fatalf("the 20 active calls spilled into the next second after 10 idle ones; they should have the whole budget")
	}
	// 30 calls started in one fleet second against a budget of 20.
}

// A caller whose own bound has run out (a long local wait used it up) reaches the Redis call with no time left and gets DeadlineExceeded. That is
// the caller's bound, not a Redis failure: it must not send the whole pod to local-only pacing for the backoff, in the middle of the surge.
func TestACallersOwnBoundRunningOutDoesNotMarkRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	b := New(Config{Redis: client(t, mr), Prefix: "t", RedisBackoff: 10 * time.Second})
	wctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if ok, _ := b.fleetSlot(wctx, true); ok {
		t.Error("a slot was reported taken with the bound already past")
	}
	if b.redisDown() {
		t.Error("the caller's expired bound marked a healthy Redis down")
	}
	if r := b.Wait(context.Background(), "x", "", true); r.Local {
		t.Errorf("a later caller was paced locally only: %+v", r)
	}
}
