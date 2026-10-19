package rivian

import (
	"context"
	"sync"
	"testing"
	"time"
)

func waitAll(ms []*StateMonitor) time.Duration {
	start := time.Now()
	var wg sync.WaitGroup
	for _, m := range ms {
		wg.Add(1)
		go func() { defer wg.Done(); m.waitStaggerSlot(context.Background()) }()
	}
	wg.Wait()
	return time.Since(start)
}

// Monitors that share a Stagger take slots one interval apart across users; with
// a Stagger each, every first connect of a single-vehicle user would go at once
// (review §4.2: the stagger was per user, so a rollout still burst).
func TestSharedStaggerSpacesFirstConnectsAcrossMonitors(t *testing.T) {
	const interval = 60 * time.Millisecond
	var own, shared []*StateMonitor
	st := NewStagger(interval)
	for i := 0; i < 4; i++ {
		a := NewStateMonitor(nil, nil)
		a.SetStagger(NewStagger(interval))
		own = append(own, a)
		b := NewStateMonitor(nil, nil)
		b.SetStagger(st)
		shared = append(shared, b)
	}
	if d := waitAll(own); d >= interval {
		t.Fatalf("separate staggers took %v for 4 first connects, want them concurrent (sanity)", d)
	}
	if d := waitAll(shared); d < 3*interval-10*time.Millisecond {
		t.Errorf("a shared stagger let 4 first connects through in %v, want at least %v apart", d, 3*interval)
	}
}

func TestRegistryGivesEveryMonitorTheSameStagger(t *testing.T) {
	r := NewMonitorRegistry(nil, nil, nil, nil, nil, nil, nil)
	if r.stagger == nil || r.stagger.interval != defaultStaggerInterval {
		t.Fatalf("registry stagger = %+v, want one at the default interval", r.stagger)
	}
	a, b := NewStateMonitor(nil, nil), NewStateMonitor(nil, nil)
	a.SetStagger(r.stagger)
	b.SetStagger(r.stagger)
	if a.stagger != b.stagger {
		t.Fatal("monitors built by the registry do not share a stagger")
	}
}

func TestStaggerStopsWaitingOnCancel(t *testing.T) {
	s := NewStagger(time.Hour)
	s.Wait(context.Background()) // takes the first slot at once
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Wait(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait ignored ctx cancellation")
	}
	var nilStagger *Stagger
	nilStagger.Wait(context.Background()) // must not panic
}
