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

	// Which vehicles this pod is subscribed to. v2's subscription closes a moment
	// after onRelease; the "stopped" event is logged under the same lock that
	// flips the flag the coordinator polls, so nothing can observe the flag
	// without the event already being in the log.
	streaming := map[string]bool{"v1": true, "v2": true}
	var smu sync.Mutex
	c := newCoordinator(st, func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(v string) { events.add("acquire " + v) },
		func(v string) {
			events.add("unsubscribe " + v)
			// The subscription takes a moment to close.
			go func() {
				time.Sleep(150 * time.Millisecond)
				smu.Lock()
				events.add("stopped " + v)
				streaming[v] = false
				smu.Unlock()
			}()
		}, nil, time.Hour)
	c.SetStreamingCheck(func(v string) bool { smu.Lock(); defer smu.Unlock(); return streaming[v] })

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

// releaseAllLogStore logs ReleaseAll into the event log.
type releaseAllLogStore struct {
	*fakeStore
	events *eventLog
}

func (s *releaseAllLogStore) ReleaseAll(ctx context.Context) (int, error) {
	s.events.add("release-all")
	return s.fakeStore.ReleaseAll(ctx)
}

// A graceful shutdown stops the subscriptions, waits until they have stopped, and
// only then releases the leases (#622). Releasing first let a peer dial a vehicle
// while this pod's connection for it was still up.
func TestShutdownStopsSubscriptionsBeforeReleasingLeases(t *testing.T) {
	events := &eventLog{}
	st := &releaseAllLogStore{fakeStore: newFakeStore(), events: events}
	streaming := map[string]bool{"v1": true, "v2": true}
	var smu sync.Mutex
	c := newCoordinator(st, func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(string) {},
		func(v string) {
			events.add("unsubscribe " + v)
			go func() {
				time.Sleep(120 * time.Millisecond)
				smu.Lock()
				events.add("stopped " + v)
				streaming[v] = false
				smu.Unlock()
			}()
		}, nil, time.Hour)
	c.SetStreamingCheck(func(v string) bool { smu.Lock(); defer smu.Unlock(); return streaming[v] })
	c.reconcile(context.Background())
	events.add("--- shutdown")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.Shutdown(ctx)

	got := events.all()
	var after []string
	for i, e := range got {
		if e == "--- shutdown" {
			after = got[i+1:]
			break
		}
	}
	pos := map[string]int{}
	for i, e := range after {
		if _, ok := pos[e]; !ok {
			pos[e] = i
		}
	}
	r, okR := pos["release-all"]
	for _, v := range []string{"v1", "v2"} {
		u, okU := pos["unsubscribe "+v]
		s, okS := pos["stopped "+v]
		if !okU || !okS || !okR || !(u < s && s < r) {
			t.Fatalf("want unsubscribe, stopped, then release-all for %s; got %v", v, after)
		}
	}
}

// A subscription that never stops cannot hold shutdown past its budget: the wait is
// capped to two thirds of the context so ReleaseAll still runs.
func TestShutdownStillReleasesWhenASubscriptionNeverStops(t *testing.T) {
	events := &eventLog{}
	st := &releaseAllLogStore{fakeStore: newFakeStore(), events: events}
	c := newCoordinator(st, func(context.Context) ([]string, error) { return []string{"v1"}, nil },
		func(string) {}, func(string) {}, nil, time.Hour)
	c.SetStreamingCheck(func(string) bool { return true }) // stuck
	c.reconcile(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.Shutdown(ctx)
	if d := time.Since(start); d > 950*time.Millisecond {
		t.Errorf("Shutdown took %v, past its 900ms budget", d)
	}
	released := false
	for _, e := range events.all() {
		if e == "release-all" {
			released = true
		}
	}
	if !released {
		t.Error("ReleaseAll never ran: the wait used the whole budget")
	}
}

// A reconcile already in flight when Shutdown begins must not resubscribe, heal or
// acquire anything: Shutdown keeps the vehicles in c.owned while it waits for the
// subscriptions to stop, and a vehicle it has just stopped looks owned-but-not-
// streaming, which healUnstreamed would restart right before the lease is released.
func TestNothingRestartsAVehicleOnceShutdownBegan(t *testing.T) {
	events := &eventLog{}
	st := &releaseAllLogStore{fakeStore: newFakeStore(), events: events}
	var c *Coordinator
	c = newCoordinator(st, func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(v string) { events.add("acquire " + v) },
		func(v string) {
			events.add("unsubscribe " + v)
			// The in-flight reconcile (and the heal inside it) runs while Shutdown waits.
			c.healUnstreamed()
			c.reconcile(context.Background())
		}, nil, time.Hour)
	c.SetStreamingCheck(func(string) bool { return false }) // everything already stopped
	c.reconcile(context.Background())                       // acquires v1 and v2
	events.add("--- shutdown")

	c.Shutdown(context.Background())

	var after []string
	got := events.all()
	for i, e := range got {
		if e == "--- shutdown" {
			after = got[i+1:]
			break
		}
	}
	for _, e := range after {
		if len(e) > 7 && e[:7] == "acquire" {
			t.Errorf("a vehicle was (re)acquired after Shutdown began: %v", after)
		}
	}
}
