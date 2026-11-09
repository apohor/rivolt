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

// Idle calls cannot use up the room active ones need. With the idle counter of this second far over its share, an active call goes through
// without waiting, and an idle call has to wait for the next second.
func TestActiveVehiclesAreNotBehindIdleOnes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	clk := newFakeClock()
	b := New(Config{LocalRPS: 1000, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", Now: clk.Now, Sleep: clk.Sleep})
	if err := mr.Set(clk.key("i"), "30"); err != nil { // thirty idle attempts this second
		t.Fatal(err)
	}
	if r := b.Wait(context.Background(), "seed", "", true); r.Starved || clk.slept() != 0 {
		t.Fatalf("an active call waited (%d sleeps) behind idle ones: %+v", clk.slept(), r)
	}
	if r := b.Wait(context.Background(), "seed", "", false); r.Starved || clk.slept() != 1 {
		t.Fatalf("an idle call over its share: %d sleeps, %+v, want exactly one wait for the next second", clk.slept(), r)
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
