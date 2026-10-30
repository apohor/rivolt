package main

import (
	"testing"

	"github.com/apohor/rivolt/internal/fence"
)

type stubFence struct{ st fence.State }

func (s stubFence) Fence(string) (fence.Token, fence.State) { return fence.Token{}, s.st }

func TestLiveActivitySourceIsFenced(t *testing.T) {
	for st, want := range map[fence.State]bool{fence.Off: true, fence.Held: true, fence.Warming: false, fence.Lost: false} {
		if got := mayPushFor(stubFence{st}, "veh"); got != want {
			t.Errorf("state %v: may push = %v, want %v", st, got, want)
		}
	}
}

func TestAtomicFenceIsOffUntilSet(t *testing.T) {
	var a atomicFence
	if _, st := a.Fence("v"); st != fence.Off {
		t.Fatalf("unset: %v, want Off", st)
	}
	a.Set(stubFence{fence.Lost})
	if _, st := a.Fence("v"); st != fence.Lost {
		t.Fatalf("after Set: %v, want Lost", st)
	}
}
