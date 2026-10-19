package bg

import (
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
