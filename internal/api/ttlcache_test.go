package api

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTTLCacheHitExpiryAndErrors(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c := newTTLCache[int](5*time.Minute, 2)
	c.now = func() time.Time { return now }
	calls := 0
	load := func() (int, error) { calls++; return calls, nil }

	if v, _ := c.get("a", load); v != 1 {
		t.Fatalf("first = %d", v)
	}
	now = now.Add(4 * time.Minute)
	if v, _ := c.get("a", load); v != 1 || calls != 1 {
		t.Fatalf("within ttl: v %d calls %d", v, calls)
	}
	now = now.Add(2 * time.Minute) // 6 min after the first load
	if v, _ := c.get("a", load); v != 2 || calls != 2 {
		t.Fatalf("after ttl: v %d calls %d", v, calls)
	}
	// Errors are not cached.
	fails := 0
	boom := func() (int, error) { fails++; return 0, errors.New("db down") }
	c.get("e", boom)
	c.get("e", boom)
	if fails != 2 {
		t.Errorf("error cached: %d loads", fails)
	}
	// Bounded: a full cache of live entries still answers new keys, uncached.
	c.get("b", load) // entries: a, b (max 2)
	before := calls
	c.get("c", load)
	c.get("c", load)
	if calls != before+2 {
		t.Errorf("overflow key was cached (calls %d -> %d)", before, calls)
	}
	if len(c.entries) > 2 {
		t.Errorf("cache grew to %d entries", len(c.entries))
	}
	// Expired entries are dropped to make room.
	now = now.Add(time.Hour)
	c.get("d", load)
	before = calls
	c.get("d", load)
	if calls != before {
		t.Error("new key not stored after expired entries were reaped")
	}
}

func TestTTLCacheSharesConcurrentLoads(t *testing.T) {
	c := newTTLCache[string](time.Minute, 4)
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var mu sync.Mutex
	loads := 0
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = c.get("k", func() (string, error) {
				mu.Lock()
				loads++
				mu.Unlock()
				started <- struct{}{}
				<-release
				return "v", nil
			})
		}(i)
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if loads != 1 {
		t.Errorf("loaded %d times for 8 concurrent callers, want 1", loads)
	}
	for i, v := range got {
		if v != "v" {
			t.Errorf("caller %d got %q", i, v)
		}
	}
}

func TestSleepActivityKeyRoundsTheWindow(t *testing.T) {
	u := uuid.New()
	t0 := time.Date(2026, 10, 16, 12, 34, 56, 789, time.UTC)
	k := sleepActivityKey(u, "v1", t0.Add(-30*24*time.Hour), t0)
	// A second later, or a few minutes later: same bucket.
	if k != sleepActivityKey(u, "v1", t0.Add(-30*24*time.Hour+time.Second), t0.Add(time.Second)) {
		t.Error("one second apart changed the key")
	}
	// A different user, vehicle, or an hour later: different keys.
	if k == sleepActivityKey(uuid.New(), "v1", t0.Add(-30*24*time.Hour), t0) ||
		k == sleepActivityKey(u, "v2", t0.Add(-30*24*time.Hour), t0) ||
		k == sleepActivityKey(u, "v1", t0.Add(-30*24*time.Hour+time.Hour), t0) {
		t.Error("distinct users/vehicles/windows share a key")
	}
}

func TestTTLCacheLoadPanicDoesNotWedgeTheKey(t *testing.T) {
	c := newTTLCache[int](time.Minute, 4)
	release := make(chan struct{})
	started := make(chan struct{})
	waiterErr := make(chan error, 1)
	leaderDone := make(chan any, 1)
	go func() {
		defer func() { leaderDone <- recover() }()
		c.get("k", func() (int, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started
	go func() {
		_, err := c.get("k", func() (int, error) { return 1, nil })
		waiterErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if p := <-leaderDone; p != "boom" {
		t.Fatalf("panic not propagated to the leader: %v", p)
	}
	select {
	case err := <-waiterErr:
		if !errors.Is(err, errTTLLoadPanicked) {
			t.Errorf("waiter err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter blocked forever after the leader panicked")
	}
	// The key is usable again.
	if v, err := c.get("k", func() (int, error) { return 7, nil }); err != nil || v != 7 {
		t.Errorf("after panic: %d %v", v, err)
	}
}
