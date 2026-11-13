package rivian

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

// byVehicle fences each vehicle on its own: a missing id is Lost.
type byVehicle map[string]struct {
	tok fence.Token
	st  fence.State
}

func (f byVehicle) Fence(id string) (fence.Token, fence.State) {
	v, ok := f[id]
	if !ok {
		return fence.Token{}, fence.Lost
	}
	return v.tok, v.st
}

// The vehicle-info persister only sees vehicles whose group this pod holds (or every vehicle with
// lead mode off), each call with its group's token; a vehicle another pod owns, or one still in
// its warm-up, is left to the owner and a Lost one is counted.
func TestVehicleInfoHookIsFenced(t *testing.T) {
	g1 := fence.Token{Group: "g1", Pod: "p", Epoch: 4}
	g2 := fence.Token{Group: "g2", Pod: "p", Epoch: 7}
	m := NewStateMonitor(nil, nil)
	lost := 0
	m.SetFence(byVehicle{
		"a": {g1, fence.Held}, "b": {g1, fence.Held}, "c": {g2, fence.Held},
		"w": {fence.Token{}, fence.Warming},
	}, func(kind string) {
		if kind == "vehicle_info" {
			lost++
		}
	})
	calls := map[fence.Token][]string{}
	n := 0
	m.persistVehicleInfo(func(ctx context.Context, vs []Vehicle) {
		n++
		tok, ok := fence.From(ctx)
		if !ok {
			t.Errorf("a call without a token: %v", vs)
		}
		for _, v := range vs {
			calls[tok] = append(calls[tok], v.ID)
		}
	}, []Vehicle{{ID: "a"}, {ID: "w"}, {ID: "b"}, {ID: "x"}, {ID: "c"}})
	if n != 2 || len(calls[g1]) != 2 || calls[g1][0] != "a" || calls[g1][1] != "b" || len(calls[g2]) != 1 || calls[g2][0] != "c" {
		t.Errorf("%d calls, %v; want a,b under g1 and c under g2", n, calls)
	}
	if lost != 1 {
		t.Errorf("lost counted %d, want 1 (x)", lost)
	}

	off := NewStateMonitor(nil, nil)
	off.SetFence(fakeFence{st: fence.Off}, func(string) {})
	var got []Vehicle
	off.persistVehicleInfo(func(ctx context.Context, vs []Vehicle) {
		if _, ok := fence.From(ctx); ok {
			t.Error("lead mode off: the call carried a token")
		}
		got = append(got, vs...)
	}, []Vehicle{{ID: "a"}, {ID: "b"}})
	if len(got) != 2 {
		t.Errorf("lead mode off: persisted %d vehicles, want 2", len(got))
	}
}

// The battery-capacity write-through runs only under the group's fence, with its token.
func TestBatteryCapacityHookIsFenced(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 2}
	for _, c := range []struct {
		st     fence.State
		calls  int
		guards bool
	}{
		{fence.Off, 1, false},
		{fence.Held, 1, true},
		{fence.Warming, 0, false},
		{fence.Lost, 0, false},
	} {
		m := NewStateMonitor(nil, nil)
		m.SetFence(fakeFence{tok: tok, st: c.st}, func(string) {})
		n := 0
		m.SetBatteryCapacityHook(func(ctx context.Context, id string, kwh float64) {
			n++
			got, ok := fence.From(ctx)
			if ok != c.guards || (ok && got != tok) {
				t.Errorf("%v: token %v %+v", c.st, ok, got)
			}
		})
		m.observeBatteryCapacity("veh", 131.5)
		if n != c.calls {
			t.Errorf("%v: hook ran %d times, want %d", c.st, n, c.calls)
		}
		// Still cached for this pod's own reads whatever the fence said.
		if v := m.vehicleInfo["veh"]; v == nil || v.PackKWh != 131.5 {
			t.Errorf("%v: capacity not cached: %+v", c.st, v)
		}
	}
}

// A vehicle whose group is still warming up when the day's refresh lands is persisted after the warm-up, from the fetched data, once the
// group is held: the refresh is not retried for a day, so dropping it left the stored gallery empty.
func TestVehicleInfoHookWritesAfterWarmUp(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 3}
	src := &switchFence{tok: tok, st: fence.Warming}
	m := NewStateMonitor(nil, nil)
	m.warmWait = 20 * time.Millisecond
	m.SetFence(src, func(string) {})
	got := make(chan Vehicle, 2)
	m.persistVehicleInfo(func(ctx context.Context, vs []Vehicle) {
		if got, ok := fence.From(ctx); !ok || got != tok {
			t.Errorf("deferred write without the group's token: %v %+v", ok, got)
		}
		for _, v := range vs {
			got2 := v
			select {
			case got <- got2:
			default:
			}
		}
	}, []Vehicle{{ID: "w", Images: []VehicleImage{{URL: "u"}}}})
	select {
	case v := <-got:
		t.Fatalf("persisted inside the warm-up: %+v", v)
	case <-time.After(5 * time.Millisecond):
	}
	src.set(fence.Held)
	select {
	case v := <-got:
		if v.ID != "w" || len(v.Images) != 1 {
			t.Errorf("persisted %+v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the gallery was not written after the warm-up")
	}
}

type switchFence struct {
	mu  sync.Mutex
	tok fence.Token
	st  fence.State
}

func (f *switchFence) set(st fence.State) { f.mu.Lock(); f.st = st; f.mu.Unlock() }
func (f *switchFence) Fence(string) (fence.Token, fence.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tok, f.st
}
