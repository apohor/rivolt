package fleetbucket

import (
	"context"
	"sort"
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

// The per-process bucket: a burst goes through at once, the next call waits for a token.
func TestLocalBucketPacesABurst(t *testing.T) {
	b := New(Config{LocalRPS: 5})
	start := time.Now()
	for i := 0; i < 5; i++ {
		if r := b.Wait(context.Background(), "dial", "", true); r.Waited > 50*time.Millisecond {
			t.Fatalf("call %d of the burst waited %v", i, r.Waited)
		}
	}
	b.Wait(context.Background(), "dial", "", true)
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("the 6th call went through after %v, want it paced (~200 ms)", d)
	}
}

// Two pods share the fleet budget: 12 calls at a budget of 4 a second cannot all start inside two seconds, however the calls are split between the pods.
func TestFleetBudgetIsSharedBetweenPods(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	const fleet = 4
	pods := []*Bucket{
		New(Config{LocalRPS: 100, FleetRPS: fleet, Redis: rdb, Prefix: "rivolt"}),
		New(Config{LocalRPS: 100, FleetRPS: fleet, Redis: rdb, Prefix: "rivolt"}),
	}
	var mu sync.Mutex
	var secs []int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if r := pods[i%2].Wait(context.Background(), "seed", "", true); r.Starved {
				t.Errorf("call %d starved", i)
			}
			mu.Lock()
			secs = append(secs, time.Now().Unix())
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	per := map[int64]int{}
	for _, s := range secs {
		per[s]++
	}
	keys := make([]int64, 0, len(per))
	for k := range per {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	// The exact bound: a counter admits at most `fleet` per second, so 12 calls cannot fit in fewer than 3 counted seconds.
	if len(keys) < 3 {
		t.Errorf("12 calls at 4/s took %d distinct seconds, want at least 3: %v", len(keys), per)
	}
}

// Idle calls cannot use up the room active ones need: with idle attempts queued, an active call still starts at once.
func TestActiveVehiclesAreNotBehindIdleOnes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	b := New(Config{LocalRPS: 100, FleetRPS: 4, Redis: rdb, Prefix: "rivolt", IdleMaxWait: 20 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 30; i++ {
		go b.Wait(ctx, "seed", "", false) // idle: at most 2 per second get through
	}
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	r := b.Wait(context.Background(), "seed", "", true)
	if r.Starved || time.Since(start) > 400*time.Millisecond {
		t.Errorf("an active call waited %v (starved %v) behind idle ones", time.Since(start), r.Starved)
	}
}

// A Redis that cannot answer is the local bucket only.
func TestRedisDownFailsOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	b := New(Config{LocalRPS: 100, FleetRPS: 1, Redis: rdb, Prefix: "rivolt"})
	mr.Close()
	start := time.Now()
	r := b.Wait(context.Background(), "dial", "", true)
	if !r.Local || r.Starved || time.Since(start) > 2*time.Second {
		t.Errorf("redis down: %+v after %v, want a local-only grant", r, time.Since(start))
	}
}

// A wait past its bound goes ahead and says so.
func TestWaitIsBoundedAndSaysStarved(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	b := New(Config{LocalRPS: 100, FleetRPS: 1, Redis: rdb, Prefix: "rivolt", ActiveMaxWait: 200 * time.Millisecond})
	b.Wait(context.Background(), "seed", "", true) // takes this second's only slot (or the next one's)
	var starved bool
	for i := 0; i < 3 && !starved; i++ {
		starved = b.Wait(context.Background(), "seed", "", true).Starved
	}
	if !starved {
		t.Error("no call was starved with a budget of 1 per second and a 200 ms bound")
	}
}

// Calls of one Rivian session's shared socket count once.
func TestSharedKeyCountsOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := client(t, mr)
	b := New(Config{LocalRPS: 100, FleetRPS: 1, Redis: rdb, Prefix: "rivolt"})
	if r := b.Wait(context.Background(), "dial", "sess-1", true); r.Shared {
		t.Fatal("the first call was shared")
	}
	for i := 0; i < 5; i++ {
		if r := b.Wait(context.Background(), "dial", "sess-1", true); !r.Shared || r.Waited != 0 {
			t.Fatalf("call %d on the same session: %+v, want shared and immediate", i, r)
		}
	}
	if r := b.Wait(context.Background(), "dial", "sess-2", true); r.Shared {
		t.Error("another session was shared")
	}
}
