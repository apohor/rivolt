package leases

import (
	"context"
	"sync"
	"testing"
	"time"
)

type triggerLog struct {
	mu  sync.Mutex
	got []string
}

func (l *triggerLog) add(s string) { l.mu.Lock(); l.got = append(l.got, s); l.mu.Unlock() }

func (l *triggerLog) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		got := append([]string(nil), l.got...)
		l.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

func runCoordinator(t *testing.T, interval time.Duration) (*Coordinator, *triggerLog) {
	t.Helper()
	c := newCoordinator(newFakeStore(), func(context.Context) ([]string, error) { return nil, nil }, func(string) {}, func(string) {}, nil, interval)
	log := &triggerLog{}
	c.SetReconcileObserver(log.add)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c, log
}

// A reconcile says what started it: the boot pass is "start", a pass asked for by the code is "other", one a peer's release signal caused is
// "signal". With a long tick no "tick" appears.
func TestReconcileIsCountedByTrigger(t *testing.T) {
	c, log := runCoordinator(t, time.Hour)
	if got := log.wait(t, 1); len(got) != 1 || got[0] != "start" {
		t.Fatalf("after boot: %v, want [start]", got)
	}
	c.TriggerReconcile()
	if got := log.wait(t, 2); len(got) != 2 || got[1] != "other" {
		t.Fatalf("after TriggerReconcile: %v, want [start other]", got)
	}
	c.onReleasedSignal()
	if got := log.wait(t, 3); len(got) != 3 || got[2] != "signal" {
		t.Fatalf("after a release signal: %v, want [start other signal]", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := log.wait(t, 3); len(got) != 3 {
		t.Errorf("more passes than asked for: %v", got)
	}
}

// The timer's pass is "tick".
func TestReconcileTimerIsATick(t *testing.T) {
	_, log := runCoordinator(t, 20*time.Millisecond)
	got := log.wait(t, 3)
	if len(got) < 3 || got[0] != "start" || got[1] != "tick" || got[2] != "tick" {
		t.Errorf("passes %v, want start then ticks", got)
	}
}
