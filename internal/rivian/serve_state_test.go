package rivian

import (
	"context"
	"testing"
	"time"
)

// ServeState: the streaming pod serves its own cache; any other pod
// serves the owner's published snapshot, falling back to a local entry
// only while it's young, never to a stale one forever.
func TestServeState(t *testing.T) {
	ctx := context.Background()
	const vid = "veh-1"
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	local := &State{VehicleID: vid, At: at("2026-10-15T19:35:01Z")}
	remote := &State{VehicleID: vid, At: at("2026-10-15T19:47:01Z")}

	setup := func(store bool, publish bool, streaming bool, age time.Duration) *StateMonitor {
		m := NewStateMonitor(nil, nil)
		if store {
			s := newMemLiveStateStore()
			if publish {
				_ = s.PutState(ctx, vid, remote, 0)
			}
			m.SetLiveStateStore(s)
		}
		m.Prime(vid, local)
		m.mu.Lock()
		m.stamp[vid] = time.Now().Add(-age)
		if streaming {
			m.active[vid] = func() {}
		}
		m.mu.Unlock()
		return m
	}
	for _, tc := range []struct {
		name                      string
		store, publish, streaming bool
		age                       time.Duration
		want                      *State
	}{
		{"owner serves its own cache, not the snapshot", true, true, true, 2 * time.Minute, local},
		{"non-owner with a primed-then-stale cache serves the snapshot", true, true, false, 12 * time.Minute, remote},
		{"non-owner prefers the snapshot even over a fresh primed entry", true, true, false, time.Second, remote},
		{"non-owner, no snapshot: a primed entry under the cutoff is served", true, false, false, primedStateMaxAge - 5*time.Second, local},
		{"non-owner, no snapshot: a primed entry past the cutoff is not", true, false, false, primedStateMaxAge + time.Second, nil},
		{"no shared store (replay mode): past the cutoff refetches", false, false, false, primedStateMaxAge + time.Second, nil},
	} {
		got := setup(tc.store, tc.publish, tc.streaming, tc.age).ServeState(ctx, vid)
		if got != tc.want && (got == nil || tc.want == nil || !got.At.Equal(tc.want.At)) {
			t.Errorf("%s: served %v, want %v", tc.name, stateAt(got), stateAt(tc.want))
		}
	}

	// A pod that loses the lease stops serving its last frame as live
	// once the owner's snapshot is there.
	m := setup(true, true, true, 0)
	m.Unsubscribe(vid)
	if got := m.ServeState(ctx, vid); got == nil || !got.At.Equal(remote.At) {
		t.Errorf("after Unsubscribe: served %v, want the snapshot %v", stateAt(got), remote.At)
	}

	var nilMon *StateMonitor
	if nilMon.ServeState(ctx, vid) != nil {
		t.Error("nil monitor must serve nothing")
	}
}

func stateAt(s *State) any {
	if s == nil {
		return nil
	}
	return s.At
}
