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
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 4}
	m := NewStateMonitor(nil, nil)
	m.warmWait = 20 * time.Millisecond
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
	m2.warmWait = 20 * time.Millisecond
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
	m3.warmWait = 20 * time.Millisecond
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

type seqFence struct {
	states []fence.State
	tok    fence.Token
	n      atomic.Int32
}

func (f *seqFence) Fence(string) (fence.Token, fence.State) {
	i := int(f.n.Add(1)) - 1
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	return f.tok, f.states[i]
}

func runAfterWarmUp(t *testing.T, states []fence.State) (wrote int32, counted int32) {
	t.Helper()
	m := NewStateMonitor(nil, nil)
	m.warmWait = 15 * time.Millisecond
	var c atomic.Int32
	m.SetFence(&seqFence{states: states, tok: fence.Token{Group: "g", Pod: "p", Epoch: 1}}, func(string) { c.Add(1) })
	var w atomic.Int32
	m.afterWarmUp("veh", func(context.Context) error { w.Add(1); return nil }, "drives")
	time.Sleep(300 * time.Millisecond)
	return w.Load(), c.Load()
}

// A second steal inside the window re-arms once; a group that stays warming or is lost drops the
// write, and the loss is counted.
func TestAfterWarmUpReArmsOnceAndCountsALoss(t *testing.T) {
	if w, _ := runAfterWarmUp(t, []fence.State{fence.Warming, fence.Held}); w != 1 {
		t.Errorf("warming then held: %d writes, want 1 (re-armed)", w)
	}
	if w, _ := runAfterWarmUp(t, []fence.State{fence.Warming, fence.Warming, fence.Held}); w != 0 {
		t.Errorf("warming twice: %d writes, want 0 (gave up after one re-arm)", w)
	}
	if w, c := runAfterWarmUp(t, []fence.State{fence.Lost}); w != 0 || c != 1 {
		t.Errorf("lost: %d writes, %d counted, want 0 and 1", w, c)
	}
}
