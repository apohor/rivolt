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
		counted      int // fenced{rivian} reports; each refusal also reports the call site
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
			if k == CallRefusedPrefix+CallSiteSeed {
				return // the per-site report, checked in TestRefusedCallsAreCountedBySite
			}
			if k != "rivian" {
				t.Errorf("%s: counted as %q", c.name, k)
			}
			n++
		})
		ctx, cancel, ok := m.rivianCtx(context.Background(), "veh", CallSiteSeed)
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
	if _, err := mk(deadlineFence{fakeFence{st: fence.Lost}, time.Now().Add(time.Minute), true}).restState(context.Background(), "veh", "test", CallSiteSeed); !errors.Is(err, fence.ErrFenced) || calls != 0 {
		t.Fatalf("lost: err=%v calls=%d, want ErrFenced and no call", err, calls)
	}
	at := time.Now().Add(10 * time.Second)
	if _, err := mk(deadlineFence{fakeFence{tok: tok, st: fence.Held}, at, true}).restState(context.Background(), "veh", "test", CallSiteSeed); err != nil || calls != 1 {
		t.Fatalf("held: err=%v calls=%d", err, calls)
	}
	if !seen.Equal(at) {
		t.Errorf("the call ran with deadline %v, want the lease's %v", seen, at)
	}
	if _, err := mk(deadlineFence{fakeFence{tok: tok, st: fence.Held}, time.Now().Add(-time.Second), true}).restState(context.Background(), "veh", "test", CallSiteSeed); !errors.Is(err, fence.ErrFenced) || calls != 1 {
		t.Fatalf("past the deadline: err=%v calls=%d, want ErrFenced and no new call", err, calls)
	}
}

// Every site the monitor reaches Rivian from reports a refusal under its own name when the pod does not hold the group, and reports
// nothing (and calls Rivian) when it does. Run per site, from a lost group, a held group past its deadline, and a held group.
func TestRefusedCallsAreCountedBySite(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 1}
	soon := time.Now().Add(time.Minute)
	for _, site := range CallSites {
		for name, src := range map[string]fence.Source{
			"lost":          deadlineFence{fakeFence{st: fence.Lost}, soon, true},
			"deadline past": deadlineFence{fakeFence{tok: tok, st: fence.Held}, time.Now().Add(-time.Second), true},
		} {
			m := NewStateMonitor(nil, nil)
			got := map[string]int{}
			m.SetFence(src, func(k string) { got[k]++ })
			_, cancel, ok := m.rivianCtx(context.Background(), "veh", site)
			cancel()
			if ok || got[CallRefusedPrefix+site] != 1 || len(got) != 2 {
				t.Errorf("%s/%s: ok=%v reports=%v, want a refusal reported once under the site (plus fenced{rivian})", site, name, ok, got)
			}
		}
		m := NewStateMonitor(nil, nil)
		got := map[string]int{}
		m.SetFence(deadlineFence{fakeFence{tok: tok, st: fence.Held}, soon, true}, func(k string) { got[k]++ })
		_, cancel, ok := m.rivianCtx(context.Background(), "veh", site)
		cancel()
		if !ok || len(got) != 0 {
			t.Errorf("%s/held: ok=%v reports=%v, want the call allowed and nothing reported", site, ok, got)
		}
	}
}

// The monitor's own REST paths each reach rivianCtx under their site: a lost group makes no request from the seed, the refresh or the
// janitor confirm, and the refusal is reported under that site.
func TestRestStateReportsItsSite(t *testing.T) {
	for _, site := range []string{CallSiteSeed, CallSiteRefreshOnce, CallSiteJanitorConfirm, CallSiteWakeSafety} {
		calls := 0
		m := NewStateMonitor(nil, nil)
		got := map[string]int{}
		m.SetFence(deadlineFence{fakeFence{st: fence.Lost}, time.Now().Add(time.Minute), true}, func(k string) { got[k]++ })
		m.restFn = func(context.Context, string) (*State, error) { calls++; return &State{}, nil }
		if _, err := m.restState(context.Background(), "veh", "test", site); !errors.Is(err, fence.ErrFenced) || calls != 0 || got[CallRefusedPrefix+site] != 1 {
			t.Errorf("%s: err=%v calls=%d reports=%v", site, err, calls, got)
		}
	}
}
