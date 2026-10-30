package rivian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/fence"
)

// The owner-triggered sends (the close hooks) run only on the pod that holds the vehicle's group:
// Held runs with the fence token in the context (so a claim made with it carries the epoch), Lost
// is skipped and counted, Warming runs once after the warm-up, lead mode off is unchanged.
func TestCloseHooksAreGatedByTheFence(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 9}
	for _, c := range []struct {
		name    string
		st      fence.State
		wantRun bool
		wantTok bool
		counted int32
	}{
		{"off", fence.Off, true, false, 0},
		{"held", fence.Held, true, true, 0},
		{"lost", fence.Lost, false, false, 1},
		{"warming", fence.Warming, false, false, 0}, // runs later; checked below
	} {
		m := NewStateMonitor(nil, nil)
		m.warmWait = 10 * time.Millisecond
		var counted atomic.Int32
		m.SetFence(fakeFence{tok: tok, st: c.st}, func(string) { counted.Add(1) })
		var ran atomic.Int32
		var gotTok atomic.Bool
		m.SetChargeCloseHook(func(ctx context.Context, _ charges.Charge) {
			ran.Add(1)
			if tk, ok := fence.From(ctx); ok && tk == tok {
				gotTok.Store(true)
			}
		})
		m.SetDriveCloseHook(func(ctx context.Context, _ drives.Drive) { ran.Add(1) })
		m.runChargeCloseHook(charges.Charge{ID: "c", VehicleID: "veh"})
		m.runDriveCloseHook(drives.Drive{ID: "d", VehicleID: "veh"})
		time.Sleep(100 * time.Millisecond)
		want := int32(0)
		if c.wantRun {
			want = 2
		}
		if got := ran.Load(); got != want {
			t.Errorf("%s: hooks ran %d times, want %d", c.name, got, want)
		}
		if gotTok.Load() != c.wantTok {
			t.Errorf("%s: token reached the hook = %v, want %v", c.name, gotTok.Load(), c.wantTok)
		}
		if counted.Load() != c.counted*2 {
			t.Errorf("%s: fenced counted %d, want %d", c.name, counted.Load(), c.counted*2)
		}
	}
}

// A hook that closes inside the warm-up is not lost: it runs once afterwards, under that moment's fence.
func TestCloseHookInTheWarmUpRunsOnceAfterIt(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 2}
	m := NewStateMonitor(nil, nil)
	m.warmWait = 20 * time.Millisecond
	m.SetFence(&seqFence{states: []fence.State{fence.Warming, fence.Held}, tok: tok}, func(string) {})
	got := make(chan fence.Token, 2)
	m.SetChargeCloseHook(func(ctx context.Context, _ charges.Charge) {
		tk, _ := fence.From(ctx)
		got <- tk
	})
	m.runChargeCloseHook(charges.Charge{ID: "c", VehicleID: "veh"})
	select {
	case tk := <-got:
		if tk != tok {
			t.Errorf("the deferred hook ran with token %+v, want %+v", tk, tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hook never ran after the warm-up")
	}
	select {
	case <-got:
		t.Error("the hook ran twice")
	case <-time.After(100 * time.Millisecond):
	}
}
