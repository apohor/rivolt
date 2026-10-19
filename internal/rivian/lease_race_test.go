package rivian

import (
	"context"
	"sync"
	"testing"
	"time"
)

// slowStopSource is a FrameSource whose streams block until cancelled
// and then wait for the test before returning, so a cancelled run
// goroutine can be held mid-teardown.
type slowStopSource struct {
	mu      sync.Mutex
	ctxs    []context.Context
	release []chan struct{}
	started chan int
}

func (s *slowStopSource) Stream(ctx context.Context, _ string, _ func(State, bool)) error {
	rel := make(chan struct{})
	s.mu.Lock()
	s.ctxs = append(s.ctxs, ctx)
	s.release = append(s.release, rel)
	n := len(s.ctxs)
	s.mu.Unlock()
	s.started <- n
	<-ctx.Done()
	<-rel
	return ctx.Err()
}

func (s *slowStopSource) stream(i int) (context.Context, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctxs[i], s.release[i]
}

func (s *slowStopSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ctxs)
}

// Lease reconcile on a Renew error: every lease is reported lost
// (Unsubscribe), then re-acquired in the same tick (EnsureSubscribed)
// before the cancelled run goroutine has exited. When it does exit it
// must leave its successor's subscription alone: the pod must still be
// able to cancel the successor, and a later EnsureSubscribed must not
// start a second stream for the vehicle.
func TestRunExitAfterSameTickReacquireKeepsTheSuccessor(t *testing.T) {
	src := &slowStopSource{started: make(chan int, 4)}
	m := NewSourceMonitor(src, nil, nil)
	m.stagger = NewStagger(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	m.parent = ctx
	m.mu.Unlock()
	waitStarted := func(want int) {
		t.Helper()
		select {
		case n := <-src.started:
			if n != want {
				t.Fatalf("stream %d started, want %d", n, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("stream %d never started", want)
		}
	}

	m.EnsureSubscribed("v") // lease acquired
	waitStarted(1)
	m.Unsubscribe("v")      // Renew error: lease "lost"
	m.EnsureSubscribed("v") // same tick: re-acquired
	waitStarted(2)

	// The first run finishes tearing down only now, after its successor
	// started.
	_, release1 := src.stream(0)
	close(release1)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		_, active := m.active["v"]
		m.mu.RUnlock()
		if !active {
			t.Fatal("the first run's exit removed its successor's active entry")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The next renew blip's re-acquire is a no-op: one stream, not two.
	m.EnsureSubscribed("v")
	time.Sleep(50 * time.Millisecond)
	if n := src.count(); n != 2 {
		t.Fatalf("%d streams started, want 2 (a second concurrent stream for the vehicle)", n)
	}

	// Losing the lease for real still stops the successor.
	m.Unsubscribe("v")
	ctx2, release2 := src.stream(1)
	if ctx2.Err() == nil {
		t.Fatal("Unsubscribe couldn't cancel the successor: the pod keeps streaming a vehicle it no longer owns")
	}
	close(release2)
}

// A run that ends without a successor still frees its slot, so a later
// EnsureSubscribed can start a new one.
func TestRunExitWithoutSuccessorFreesTheSlot(t *testing.T) {
	src := &slowStopSource{started: make(chan int, 4)}
	m := NewSourceMonitor(src, nil, nil)
	m.stagger = NewStagger(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	m.parent = ctx
	m.mu.Unlock()

	m.EnsureSubscribed("v")
	<-src.started
	m.Unsubscribe("v")
	_, release1 := src.stream(0)
	close(release1)

	m.EnsureSubscribed("v")
	select {
	case <-src.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no new stream after the previous run exited")
	}
	m.Unsubscribe("v")
	_, release2 := src.stream(1)
	close(release2)
}
