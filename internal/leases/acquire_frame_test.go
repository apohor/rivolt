package leases

import (
	"context"
	"testing"
	"time"
)

// Acquire-to-first-frame is reported once, from the frame's own arrival time, and a vehicle that is
// released before it shows a frame is forgotten.
func TestAcquireToFrameIsObservedOnce(t *testing.T) {
	fs := newFakeStore()
	now := time.Unix(2_000_000, 0)
	frames := map[string]time.Time{}
	var got []float64
	vehicles := []string{"v1", "v2"}
	c := newCoordinator(fs, func(context.Context) ([]string, error) { return vehicles, nil },
		func(string) {}, func(string) {}, nil, time.Hour)
	c.now = func() time.Time { return now }
	c.SetFrameObserver(func(v string) time.Time { return frames[v] }, func(s float64) { got = append(got, s) })

	c.reconcile(context.Background()) // acquires v1 and v2 at t0
	if len(got) != 0 {
		t.Fatalf("observed before any frame: %v", got)
	}
	t0 := now
	frames["v1"] = t0.Add(9 * time.Second)
	now = t0.Add(10 * time.Second)
	c.reconcile(context.Background())
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("observed %v, want [9] (the frame's arrival, not the tick's)", got)
	}
	now = t0.Add(20 * time.Second)
	c.reconcile(context.Background())
	if len(got) != 1 {
		t.Fatalf("observed twice: %v", got)
	}

	// A frame from before the acquire (a previous owner's cache) is not a first frame.
	frames["v2"] = t0.Add(-5 * time.Second)
	now = t0.Add(30 * time.Second)
	c.reconcile(context.Background())
	if len(got) != 1 {
		t.Fatalf("a frame older than the acquire counted: %v", got)
	}
	// Released: forgotten, never observed afterwards.
	fs.mu.Lock()
	delete(fs.owned, "v2")
	fs.mu.Unlock()
	vehicles = []string{"v1"} // and not wanted any more
	now = t0.Add(40 * time.Second)
	c.reconcile(context.Background())
	frames["v2"] = t0.Add(41 * time.Second)
	now = t0.Add(50 * time.Second)
	c.reconcile(context.Background())
	if len(got) != 1 {
		t.Fatalf("observed a released vehicle: %v", got)
	}
}
