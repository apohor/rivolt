package leases

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// qa's host-pause run: after CONT the pod logged "lease renewals stopped working for the local deadline: stopping every
// subscription" and three "lease fenced" lines, and rivolt_lease_self_fence_total stayed 0, so the alert could not fire. Every path
// that stops everything because renews ran out counts, once per event (not once per vehicle, not once for each path that finds it).
func TestSelfFenceCountsOncePerEventOnTheLocalDeadlinePath(t *testing.T) {
	for _, c := range []struct {
		name     string
		renewErr error // the renew after the pause: nil works, an error is the second path finding the same event
	}{
		{"the renew works after the pause", nil},
		{"the renew also fails after the pause", errors.New("db down")},
	} {
		fs := newFakeStore()
		fs.owned["v2"], fs.owned["v3"] = true, true
		now := time.Unix(1_000_000, 0)
		var released []string
		co := newCoordinator(ttlStore{fs}, func(context.Context) ([]string, error) { return []string{"v1", "v2", "v3"}, nil },
			func(string) {}, func(v string) { released = append(released, v) }, nil, 5*time.Second)
		co.jitter = time.Second // lead mode
		co.now = func() time.Time { return now }
		fences := 0
		co.SetSelfFenceObserver(func() { fences++ })

		co.reconcile(context.Background()) // t0: three vehicles held, a good renew
		if len(co.owned) != 3 || fences != 0 {
			t.Fatalf("%s: owned %d, fences %d before the pause", c.name, len(co.owned), fences)
		}
		now = now.Add(21 * time.Second) // a frozen process wakes: past the 20 s local deadline
		fs.renewErr = c.renewErr
		co.reconcile(context.Background())
		if len(released) < 3 {
			t.Fatalf("%s: stopped %v, want all three subscriptions", c.name, released)
		}
		if fences != 1 {
			t.Errorf("%s: self-fence counted %d times for one event with three vehicles, want exactly 1", c.name, fences)
		}
		fs.renewErr = nil
		co.reconcile(context.Background())
		co.reconcile(context.Background())
		if fences != 1 {
			t.Errorf("%s: counted again with nothing new to stop: %d", c.name, fences)
		}
		// A later, separate pause (the three vehicles were re-acquired by the passes above) is a second event.
		now = now.Add(21 * time.Second)
		co.reconcile(context.Background())
		if fences != 2 {
			t.Errorf("%s: a second pause counted %d total, want 2", c.name, fences)
		}
	}
}

// rivolt_lease_renew_total{scope,outcome}: what qa could not see on the renew path.
func TestRenewOutcomesAreCountedByScope(t *testing.T) {
	var got []string
	record := func(c *Coordinator) {
		c.SetRenewObserver(func(scope, outcome string) { got = append(got, scope+":"+outcome) })
	}

	// Vehicle scope (the non-lead coordinator): ok, lost, timeout, error.
	fs := newFakeStore()
	c := newCoordinator(fs, func(context.Context) ([]string, error) { return []string{"v1"}, nil }, func(string) {}, func(string) {}, nil, 5*time.Second)
	record(c)
	c.reconcile(context.Background()) // acquires v1, renews nothing yet or renews: ok
	fs.mu.Lock()
	fs.stealVeh = "v1" // dropped from the renew result: the renew worked and we lost one
	fs.mu.Unlock()
	c.reconcile(context.Background())
	fs.renewErr = context.DeadlineExceeded
	c.reconcile(context.Background())
	fs.renewErr = errors.New("boom")
	c.reconcile(context.Background())
	want := []string{"vehicle:ok", "vehicle:lost", "vehicle:timeout", "vehicle:error"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vehicle renews = %v, want %v", got, want)
	}

	// Group scope (lead mode): ok, lost, timeout, error. Each pass also renews the vehicle leases, so only group entries are compared.
	got = nil
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"}, res: GroupLead{Held: map[string]int64{"g1": 1}}}
	lc := newLeadCoord(ls, []string{"v1"})
	record(lc)
	lc.reconcile(context.Background())
	ls.lmu.Lock()
	ls.res.Lost = []string{"g9"}
	ls.lmu.Unlock()
	lc.reconcile(context.Background())
	ls.lmu.Lock()
	ls.res.Lost = nil
	ls.leadErr = context.DeadlineExceeded
	ls.lmu.Unlock()
	lc.reconcile(context.Background())
	ls.lmu.Lock()
	ls.leadErr = errors.New("boom")
	ls.lmu.Unlock()
	lc.reconcile(context.Background())
	var groups []string
	for _, g := range got {
		if len(g) > 6 && g[:6] == "group:" {
			groups = append(groups, g)
		}
	}
	if want := []string{"group:ok", "group:lost", "group:timeout", "group:error"}; !reflect.DeepEqual(groups, want) {
		t.Errorf("group renews = %v, want %v (all: %v)", groups, want, got)
	}
}
