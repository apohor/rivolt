package leases

import (
	"context"
	"sync"
	"testing"
	"time"
)

// splitStore is a fakeStore that reports a split user and records the order of
// events with the coordinator's callbacks.
type splitStore struct {
	*fakeStore
	split  []string
	events *eventLog
}

func (s *splitStore) SplitVehicles(context.Context) ([]string, error) { return s.split, nil }

func (s *splitStore) Release(ctx context.Context, vid string) error {
	s.events.add("release " + vid)
	return s.fakeStore.Release(ctx, vid)
}

type eventLog struct {
	mu sync.Mutex
	e  []string
}

func (l *eventLog) add(s string) { l.mu.Lock(); l.e = append(l.e, s); l.mu.Unlock() }
func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.e...)
}

// A pod that finds one of its users split onto a lower pod id gives the vehicle
// up in the order: stop the subscription, wait until it has stopped, then release
// the lease. Releasing first would let the other pod dial while this pod's
// connection is still up.
func TestCoordinatorHealsASplitUserStopThenWaitThenRelease(t *testing.T) {
	events := &eventLog{}
	fs := newFakeStore()
	st := &splitStore{fakeStore: fs, events: events}

	streaming := true
	var smu sync.Mutex
	c := newCoordinator(st, func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(v string) { events.add("acquire " + v) },
		func(v string) {
			events.add("unsubscribe " + v)
			// The subscription takes a moment to close.
			go func() {
				time.Sleep(150 * time.Millisecond)
				smu.Lock()
				streaming = false
				smu.Unlock()
				events.add("stopped " + v)
			}()
		}, nil, time.Hour)
	c.SetStreamingCheck(func(string) bool { smu.Lock(); defer smu.Unlock(); return streaming })

	c.reconcile(context.Background()) // acquires v1 and v2
	st.split = []string{"v2"}
	// The real store refuses v2 now: the user's other vehicle is on the lower pod.
	fs.mu.Lock()
	fs.acquireOK = func(v string) bool { return v != "v2" }
	fs.mu.Unlock()
	events.add("--- split found")
	c.reconcile(context.Background())

	got := events.all()
	var after []string
	for i, e := range got {
		if e == "--- split found" {
			after = got[i+1:]
			break
		}
	}
	// Order of the three events for v2, whatever else the tick does.
	pos := map[string]int{}
	for i, e := range after {
		if _, ok := pos[e]; !ok {
			pos[e] = i
		}
	}
	u, okU := pos["unsubscribe v2"]
	s, okS := pos["stopped v2"]
	r, okR := pos["release v2"]
	if !okU || !okS || !okR || !(u < s && s < r) {
		t.Fatalf("want unsubscribe, then stopped, then release for v2; got %v", after)
	}
	c.mu.Lock()
	_, stillOwned := c.owned["v2"]
	_, keptV1 := c.owned["v1"]
	c.mu.Unlock()
	if stillOwned || !keptV1 {
		t.Errorf("owned after healing: v2=%v v1=%v, want v2 given up and v1 kept", stillOwned, keptV1)
	}
}

// Without a split nothing is released.
func TestCoordinatorLeavesAWholeUserAlone(t *testing.T) {
	events := &eventLog{}
	st := &splitStore{fakeStore: newFakeStore(), events: events}
	c := newCoordinator(st, func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(string) {}, func(v string) { events.add("unsubscribe " + v) }, nil, time.Hour)
	c.reconcile(context.Background())
	c.reconcile(context.Background())
	if got := events.all(); len(got) != 0 {
		t.Errorf("events with no split: %v", got)
	}
}
