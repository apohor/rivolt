package rivian

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ServeState: the streaming pod serves its own cache; any other pod
// serves the owner's published snapshot, and without one serves what it
// has, fetching from Rivian at most once per stateRefetchEvery and never
// while that state says asleep.
func TestServeState(t *testing.T) {
	ctx := context.Background()
	const vid = "veh-1"
	t0 := time.Date(2026, 10, 15, 19, 35, 1, 0, time.UTC)
	local := &State{VehicleID: vid, At: t0, PowerState: "ready"}
	asleep := &State{VehicleID: vid, At: t0, PowerState: "sleep"}
	remote := &State{VehicleID: vid, At: t0.Add(12 * time.Minute), PowerState: "ready"}

	type setup struct {
		store, publish, streaming bool
		cached                    *State
		lastFetch                 time.Duration // 0: never fetched
	}
	mk := func(s setup) *StateMonitor {
		m := NewStateMonitor(nil, nil)
		if s.store {
			st := newMemLiveStateStore()
			if s.publish {
				_ = st.PutState(ctx, vid, remote, 0)
			}
			m.SetLiveStateStore(st)
		}
		if s.cached != nil {
			m.Prime(vid, s.cached)
		}
		m.mu.Lock()
		if s.streaming {
			m.active[vid] = func() {}
		}
		m.mu.Unlock()
		if s.lastFetch > 0 {
			m.fallbackAt = map[string]time.Time{vid: time.Now().Add(-s.lastFetch)}
		}
		return m
	}
	for _, tc := range []struct {
		name      string
		s         setup
		want      *State
		wantFetch bool
	}{
		{"owner serves its own cache, not the snapshot", setup{true, true, true, local, 0}, local, false},
		{"non-owner with a primed-then-stale cache serves the snapshot", setup{true, true, false, local, 0}, remote, false},
		{"no snapshot, never fetched: fetch (serve the entry if that fails)", setup{true, false, false, local, 0}, local, true},
		{"no snapshot, fetched 1 min ago: serve the entry, no fetch", setup{true, false, false, local, time.Minute}, local, false},
		{"no snapshot, fetched past the limit: fetch again", setup{true, false, false, local, stateRefetchEvery + time.Second}, local, true},
		{"no snapshot, entry asleep: never fetch, however old", setup{true, false, false, asleep, 12 * time.Hour}, asleep, false},
		{"no shared store (replay mode), nothing cached: fetch", setup{false, false, false, nil, 0}, nil, true},
		{"nothing cached, a fetch tried 1 min ago: neither", setup{false, false, false, nil, time.Minute}, nil, false},
	} {
		got, fetch := mk(tc.s).ServeState(ctx, vid)
		if fetch != tc.wantFetch || (got == nil) != (tc.want == nil) || (got != nil && !got.At.Equal(tc.want.At)) {
			t.Errorf("%s: served %v fetch=%v, want %v fetch=%v", tc.name, stateAt(got), fetch, stateAt(tc.want), tc.wantFetch)
		}
	}

	// A pod that loses the lease serves the owner's snapshot, not its
	// own last frame.
	m := mk(setup{true, true, true, local, 0})
	m.Unsubscribe(vid)
	if got, fetch := m.ServeState(ctx, vid); fetch || got == nil || !got.At.Equal(remote.At) {
		t.Errorf("after Unsubscribe: served %v fetch=%v, want the snapshot %v", stateAt(got), fetch, remote.At)
	}

	// Concurrent polls with no snapshot: one fetch between them.
	m = mk(setup{false, false, false, local, 0})
	var wg sync.WaitGroup
	var mu sync.Mutex
	fetches := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, f := m.ServeState(ctx, vid); f {
				mu.Lock()
				fetches++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if fetches != 1 {
		t.Errorf("20 concurrent polls fetched %d times, want 1", fetches)
	}
}

func stateAt(s *State) any {
	if s == nil {
		return nil
	}
	return s.At
}
