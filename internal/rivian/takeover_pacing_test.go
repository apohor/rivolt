package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fleetbucket"
)

// A vehicle is active for the pacing when the previous owner's snapshot holds an open drive or charge; an idle one, or no snapshot, is not.
func TestTakeoverActiveFromTheSnapshot(t *testing.T) {
	store := newMemLiveStateStore()
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	ctx := context.Background()
	if m.takeoverActive(ctx, "v-1") {
		t.Error("no snapshot: active")
	}
	_ = store.Save(ctx, "v-1", LiveStateSnapshot{Drive: &LiveDriveSnapshot{ID: "d"}}, time.Minute)
	_ = store.Save(ctx, "v-2", LiveStateSnapshot{Charge: &LiveChargeSnapshot{ID: "c"}}, time.Minute)
	_ = store.Save(ctx, "v-3", LiveStateSnapshot{DriveCounter: 4}, time.Minute)
	for vid, want := range map[string]bool{"v-1": true, "v-2": true, "v-3": false} {
		if got := m.takeoverActive(ctx, vid); got != want {
			t.Errorf("%s: active %v, want %v", vid, got, want)
		}
	}
}

// No limiter: no pacing and no panic. With one, the second call within a burst of 1 waits for its token.
func TestTakeoverWaitPaces(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	start := time.Now()
	m.takeoverWait(context.Background(), "seed", "", true, "v-1")
	if time.Since(start) > 20*time.Millisecond {
		t.Error("no limiter still waited")
	}
	m.SetTakeoverLimiter(fleetbucket.New(fleetbucket.Config{LocalRPS: 2}))
	start = time.Now()
	for i := 0; i < 4; i++ { // burst 2, then 500 ms apiece
		m.takeoverWait(context.Background(), "seed", "", true, "v-1")
	}
	if d := time.Since(start); d < 800*time.Millisecond {
		t.Errorf("4 calls at 2/s took %v, want about 1 s", d)
	}
}

// A slow roll logs one line a minute per class, with the number of calls it stands for.
func TestStarvedLogIsOneLineAMinutePerClass(t *testing.T) {
	r := &rateLimitedCount{every: time.Minute}
	t0 := time.Now()
	if n, ok := r.note("seed", t0); !ok || n != 1 {
		t.Fatalf("first: %d %v, want a line for 1", n, ok)
	}
	for i := 0; i < 50; i++ {
		if _, ok := r.note("seed", t0.Add(time.Duration(i)*time.Second/2)); ok {
			t.Fatal("logged again inside the minute")
		}
	}
	if n, ok := r.note("dial", t0.Add(time.Second)); !ok || n != 1 {
		t.Errorf("another class is its own line: %d %v", n, ok)
	}
	if n, ok := r.note("seed", t0.Add(61*time.Second)); !ok || n != 51 {
		t.Errorf("after the minute: %d %v, want a line for the 51 calls since", n, ok)
	}
}
