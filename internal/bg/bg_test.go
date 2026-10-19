package bg

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitReturnsOnceEveryGoroutineFinished(t *testing.T) {
	var g Group
	var done atomic.Int32
	for i := 0; i < 5; i++ {
		g.Go(func() { time.Sleep(60 * time.Millisecond); done.Add(1) })
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("Wait timed out with goroutines that finish")
	}
	if done.Load() != 5 {
		t.Errorf("%d of 5 finished before Wait returned", done.Load())
	}
}

func TestWaitIsBounded(t *testing.T) {
	var g Group
	release := make(chan struct{})
	defer close(release)
	g.Go(func() { <-release })
	start := time.Now()
	if g.Wait(100 * time.Millisecond) {
		t.Fatal("Wait reported done with a goroutine still running")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Wait took %v, want about its 100ms bound", d)
	}
}

func TestWaitWithNothingRunning(t *testing.T) {
	var g Group
	if !g.Wait(time.Second) {
		t.Fatal("empty group did not return at once")
	}
}

// Work started while Wait is already running (a close hook firing during the
// drain) is tracked, not a panic, and Wait returns only when that work is done too.
func TestGoDuringWaitIsTrackedAndDoesNotPanic(t *testing.T) {
	var g Group
	var done atomic.Int32
	g.Go(func() {
		time.Sleep(50 * time.Millisecond)
		g.Go(func() { time.Sleep(50 * time.Millisecond); done.Add(1) }) // starts as the count would hit zero
	})
	if !g.Wait(5 * time.Second) {
		t.Fatal("Wait timed out")
	}
	if done.Load() != 1 {
		t.Errorf("work started during Wait had not finished when Wait returned (%d)", done.Load())
	}
}

func TestConcurrentGoAndWait(t *testing.T) {
	var g Group
	stop := make(chan struct{})
	var spawned atomic.Int32
	started := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				g.Go(func() { spawned.Add(1) })
				once.Do(func() { close(started) })
			}
		}
	}()
	<-started // the spawner is running, so the Waits below overlap with Go
	for i := 0; i < 200; i++ {
		g.Wait(time.Millisecond)
	}
	close(stop)
	if !g.Wait(5 * time.Second) {
		t.Fatal("never went idle")
	}
	if spawned.Load() == 0 {
		t.Error("no work ran")
	}
}
