package rivian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

// A final write that lands in the post-steal warm-up (a session that has closed, so no later frame
// repeats it) is made once, after the warm-up, under the fence of that moment.
func TestAfterWarmUpWritesOnceUnderTheFenceOfThatMoment(t *testing.T) {
	old := warmUpRetry
	warmUpRetry = 20 * time.Millisecond
	defer func() { warmUpRetry = old }()

	tok := fence.Token{Group: "g", Pod: "p", Epoch: 4}
	m := NewStateMonitor(nil, nil)
	m.SetFence(fakeFence{tok: tok, st: fence.Held}, func(string) {})
	var wrote atomic.Int32
	var got atomic.Value
	done := make(chan struct{})
	m.afterWarmUp("veh", func(ctx context.Context) error {
		wrote.Add(1)
		got.Store(ctx)
		close(done)
		return nil
	}, "drives")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the deferred write never ran")
	}
	if tk, ok := fence.From(got.Load().(context.Context)); !ok || tk != tok {
		t.Errorf("the deferred write ran with token %v %+v, want %+v", ok, tk, tok)
	}

	// Lost by then: dropped, not written.
	m2 := NewStateMonitor(nil, nil)
	lost := 0
	m2.SetFence(fakeFence{st: fence.Lost}, func(string) { lost++ })
	ran := make(chan struct{}, 1)
	m2.afterWarmUp("veh", func(context.Context) error { ran <- struct{}{}; return nil }, "drives")
	select {
	case <-ran:
		t.Error("a write ran for a group this pod no longer holds")
	case <-time.After(300 * time.Millisecond):
	}

	// Refused by the database: counted.
	m3 := NewStateMonitor(nil, nil)
	fenced := make(chan string, 1)
	m3.SetFence(fakeFence{tok: tok, st: fence.Held}, func(k string) { fenced <- k })
	m3.afterWarmUp("veh", func(context.Context) error { return fence.ErrFenced }, "drives")
	select {
	case k := <-fenced:
		if k != "drives" {
			t.Errorf("counted as %q", k)
		}
	case <-time.After(2 * time.Second):
		t.Error("an ErrFenced from the deferred write was not counted")
	}
}
