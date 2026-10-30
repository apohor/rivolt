package rivian

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

type deadlineFence struct {
	fakeFence
	at  time.Time
	has bool
}

func (d deadlineFence) Deadline(string) (time.Time, bool) { return d.at, d.has }

// Every Rivian call is made under the vehicle's local lease deadline, only by the pod that holds the
// group; a call after the deadline (a pause, a failed renew) never goes out.
func TestRivianCtxByFenceStateAndDeadline(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 1}
	soon := time.Now().Add(15 * time.Second)
	for _, c := range []struct {
		name         string
		src          fence.Source
		ok           bool
		wantDeadline bool
		counted      int
	}{
		{"no fence", nil, true, false, 0},
		{"lead off", deadlineFence{fakeFence: fakeFence{st: fence.Off}}, true, false, 0},
		{"held, deadline ahead", deadlineFence{fakeFence{tok: tok, st: fence.Held}, soon, true}, true, true, 0},
		{"warming connects at once", deadlineFence{fakeFence{tok: tok, st: fence.Warming}, soon, true}, true, true, 0},
		{"held, deadline passed", deadlineFence{fakeFence{tok: tok, st: fence.Held}, time.Now().Add(-time.Second), true}, false, false, 1},
		{"lost", deadlineFence{fakeFence{st: fence.Lost}, soon, true}, false, false, 1},
	} {
		m := NewStateMonitor(nil, nil)
		n := 0
		m.SetFence(c.src, func(k string) {
			if k != "rivian" {
				t.Errorf("%s: counted as %q", c.name, k)
			}
			n++
		})
		ctx, cancel, ok := m.rivianCtx(context.Background(), "veh")
		cancel()
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
		}
		if _, has := ctx.Deadline(); has != c.wantDeadline {
			t.Errorf("%s: has deadline = %v, want %v", c.name, has, c.wantDeadline)
		}
		if c.wantDeadline {
			if d, _ := ctx.Deadline(); !d.Equal(soon) {
				t.Errorf("%s: deadline %v, want the lease's %v", c.name, d, soon)
			}
		}
		if n != c.counted {
			t.Errorf("%s: counted %d, want %d", c.name, n, c.counted)
		}
	}
}

// restState (the seed, the periodic refresh, the hold check, the stale-charge confirm) calls Rivian
// only under the fence: a pod that lost the group makes no request.
func TestRestStateIsFenced(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 1}
	calls := 0
	var seen time.Time
	mk := func(src fence.Source) *StateMonitor {
		m := NewStateMonitor(nil, nil)
		m.SetFence(src, func(string) {})
		m.restFn = func(ctx context.Context, _ string) (*State, error) {
			calls++
			seen, _ = ctx.Deadline()
			return &State{VehicleID: "veh"}, nil
		}
		return m
	}
	if _, err := mk(deadlineFence{fakeFence{st: fence.Lost}, time.Now().Add(time.Minute), true}).restState(context.Background(), "veh", "test"); !errors.Is(err, fence.ErrFenced) || calls != 0 {
		t.Fatalf("lost: err=%v calls=%d, want ErrFenced and no call", err, calls)
	}
	at := time.Now().Add(10 * time.Second)
	if _, err := mk(deadlineFence{fakeFence{tok: tok, st: fence.Held}, at, true}).restState(context.Background(), "veh", "test"); err != nil || calls != 1 {
		t.Fatalf("held: err=%v calls=%d", err, calls)
	}
	if !seen.Equal(at) {
		t.Errorf("the call ran with deadline %v, want the lease's %v", seen, at)
	}
	if _, err := mk(deadlineFence{fakeFence{tok: tok, st: fence.Held}, time.Now().Add(-time.Second), true}).restState(context.Background(), "veh", "test"); !errors.Is(err, fence.ErrFenced) || calls != 1 {
		t.Fatalf("past the deadline: err=%v calls=%d, want ErrFenced and no new call", err, calls)
	}
}
