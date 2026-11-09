package fleetbucket

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func client(t *testing.T, mr *miniredis.Miniredis) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// fakeClock is the time the budget's seconds are counted in. Sleep does not wait: it moves the clock, so what the bucket does at each second
// is decided by the test and not by how busy the machine is.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
}

func (c *fakeClock) slept() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sleeps)
}

func (c *fakeClock) key(kind string) string {
	return fmt.Sprintf("rivolt:rivian:takeover:%s:%d", kind, c.Now().Unix())
}

// The per-process bucket: after its burst a call waits for a token (a lower bound only: how long a call took is not asserted, a loaded machine
// may take longer).
func TestLocalBucketPacesABurst(t *testing.T) {
	b := New(Config{LocalRPS: 5})
	start := time.Now()
	for i := 0; i < 5; i++ {
		b.Wait(context.Background(), "dial", "", true)
	}
	b.Wait(context.Background(), "dial", "", true)
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("the 6th call went through after %v, want it paced (about 200 ms)", d)
	}
}

// Two pods share the fleet budget: with 4 a second, 12 calls take exactly three seconds of the budget and no second takes more than 4, however the
// calls are split between the pods.
func TestFleetBudgetIsSharedBetweenPods(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	mk := func() *Bucket {
		return New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep})
	}
	pods := []*Bucket{mk(), mk()}
	per := map[int64]int{}
	for i := 0; i < 12; i++ {
		r := pods[i%2].Wait(context.Background(), "seed", "", true)
		if r.Starved || r.Local {
			t.Fatalf("call %d: %+v", i, r)
		}
		per[clk.Now().Unix()]++
	}
	if len(per) != 3 {
		t.Errorf("12 calls at 4 a second used %d seconds, want 3: %v", len(per), per)
	}
	for sec, n := range per {
		if n > 4 {
			t.Errorf("second %d granted %d calls, budget 4: %v", sec, n, per)
		}
	}
}

// Idle calls cannot use up the room active ones need. The counters hold GRANTED calls: with the idle share of this second used up (2 of a budget of
// 4), an active call goes through without waiting, and an idle call has to wait for the next second.
func TestActiveVehiclesAreNotBehindIdleOnes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep})
	if err := mr.Set(clk.key("i"), "2"); err != nil { // idle has had its half
		t.Fatal(err)
	}
	if r := b.Wait(context.Background(), "seed", "", true); r.Starved || clk.slept() != 0 {
		t.Fatalf("an active call waited (%d sleeps) behind idle ones: %+v", clk.slept(), r)
	}
	if r := b.Wait(context.Background(), "seed", "", false); r.Starved || clk.slept() != 1 {
		t.Fatalf("an idle call over its share: %d sleeps, %+v, want exactly one wait for the next second", clk.slept(), r)
	}
}

// Active and idle together never pass the budget in a second, and idle never passes half of it. Mixed calls in a row on a fake clock: no second
// grants more than 4 in all or more than 2 idle.
func TestCombinedBudgetHoldsAcrossSeconds(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep})
	total, idle := map[int64]int{}, map[int64]int{}
	for i := 0; i < 24; i++ {
		isActive := i%3 != 0 // one idle call for every two active
		if r := b.Wait(context.Background(), "seed", "", isActive); r.Starved {
			t.Fatalf("call %d starved", i)
		}
		sec := clk.Now().Unix()
		total[sec]++
		if !isActive {
			idle[sec]++
		}
	}
	for sec, n := range total {
		if n > 4 {
			t.Errorf("second %d granted %d calls, budget 4: %v", sec, n, total)
		}
		if idle[sec] > 2 {
			t.Errorf("second %d granted %d idle calls, share 2", sec, idle[sec])
		}
	}
	if len(total) < 6 {
		t.Errorf("24 calls at 4 a second used %d seconds, want at least 6: %v", len(total), total)
	}
}

// The same bound when the callers race: on a frozen clock (the wait for the next second never ends, so the bound is what ends it) many active and
// idle callers at once are granted no more than the budget in all, and idle no more than its half. Granted means not starved.
func TestCombinedBudgetHoldsUnderRacingCallers(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now,
		Sleep:         func(ctx context.Context, _ time.Duration) { <-ctx.Done() },
		ActiveMaxWait: 100 * time.Millisecond, IdleMaxWait: 100 * time.Millisecond})
	var wg sync.WaitGroup
	var mu sync.Mutex
	grantedActive, grantedIdle := 0, 0
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(active bool) {
			defer wg.Done()
			if r := b.Wait(context.Background(), "seed", "", active); !r.Starved {
				mu.Lock()
				if active {
					grantedActive++
				} else {
					grantedIdle++
				}
				mu.Unlock()
			}
		}(i%2 == 0)
	}
	wg.Wait()
	if grantedActive+grantedIdle > 4 || grantedIdle > 2 {
		t.Errorf("granted %d active and %d idle in one second, want at most 4 in all and 2 idle", grantedActive, grantedIdle)
	}
	if grantedActive+grantedIdle == 0 {
		t.Error("nothing was granted")
	}
}

// A Redis that cannot answer is the local bucket only.
func TestRedisDownFailsOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	b := New(Config{LocalRPS: 100, FleetRPS: 1, Redis: rdb, Prefix: "rivolt"})
	mr.Close()
	if r := b.Wait(context.Background(), "dial", "", true); !r.Local || r.Starved {
		t.Errorf("redis down: %+v, want a local-only grant", r)
	}
}

// A wait past its bound goes ahead and says so. The second's budget is already used up and the wait for the next second never ends (a Sleep
// that only returns when its context does), so the bound is what ends it.
func TestWaitIsBoundedAndSaysStarved(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 1, Redis: rdb, Prefix: "rivolt", Now: clk.Now,
		Sleep: func(ctx context.Context, _ time.Duration) { <-ctx.Done() }, ActiveMaxWait: 30 * time.Millisecond})
	if err := mr.Set(clk.key("a"), "5"); err != nil {
		t.Fatal(err)
	}
	if r := b.Wait(context.Background(), "seed", "", true); !r.Starved {
		t.Errorf("%+v, want starved", r)
	}
}

// Calls of one Rivian session's shared socket count once.
func TestSharedKeyCountsOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 1, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep})
	if r := b.Wait(context.Background(), "dial", "sess-1", true); r.Shared {
		t.Fatal("the first call was shared")
	}
	for i := 0; i < 5; i++ {
		if r := b.Wait(context.Background(), "dial", "sess-1", true); !r.Shared || clk.slept() != 0 {
			t.Fatalf("call %d on the same session: %+v (%d sleeps), want shared and immediate", i, r, clk.slept())
		}
	}
	if r := b.Wait(context.Background(), "dial", "sess-2", true); r.Shared {
		t.Error("another session was shared")
	}
	// The share lapses with the clock, not the wall.
	clk.Sleep(context.Background(), 31*time.Second)
	if r := b.Wait(context.Background(), "dial", "sess-1", true); r.Shared {
		t.Error("a call after the share's time was still shared")
	}
}

// Observe is told the outcome of every Wait, once: the class and priority as given, and whether it waited, shared a session's slot, starved or
// went local-only.
func TestObserveHearsEveryWait(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	type seen struct {
		class  string
		active bool
		r      Result
	}
	var got []seen
	b := New(Config{LocalRPS: 1000, FleetRPS: 1, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep,
		Observe: func(class string, active bool, r Result) { got = append(got, seen{class, active, r}) }})
	b.Wait(context.Background(), "dial", "s1", true)
	b.Wait(context.Background(), "dial", "s1", true) // shared
	b.Wait(context.Background(), "seed", "", false)  // the second's budget is used by the dial: waits for the next second
	if len(got) != 3 {
		t.Fatalf("%d observations for 3 waits", len(got))
	}
	if got[0].class != "dial" || !got[0].active || got[0].r.Shared || got[0].r.Starved {
		t.Errorf("first: %+v", got[0])
	}
	if !got[1].r.Shared {
		t.Errorf("second should be shared: %+v", got[1])
	}
	if got[2].class != "seed" || got[2].active || got[2].r.Waited <= 0 {
		t.Errorf("third should be an idle seed that waited for the next second: %+v", got[2])
	}
	var nilBucket *Bucket
	nilBucket.Wait(context.Background(), "dial", "", true) // a nil bucket neither waits nor panics
}
