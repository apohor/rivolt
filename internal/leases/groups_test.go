package leases

import (
	"context"
	"errors"
	"testing"
	"time"
)

// groupFake adds RenewGroups to the fake store.
type groupFake struct {
	*fakeStore
	res   GroupResult
	err   error
	calls int
}

func (g *groupFake) RenewGroups(context.Context) (GroupResult, error) {
	g.calls++
	return g.res, g.err
}

func TestCoordinatorRenewsGroupsAndReportsDryRun(t *testing.T) {
	t.Parallel()
	g := &groupFake{fakeStore: newFakeStore(), res: GroupResult{Held: 2, DryRunRejected: []string{"g1"}}}
	var held, dry int
	c := newCoordinator(g, func(context.Context) ([]string, error) { return nil, nil },
		func(string) {}, func(string) {}, nil, time.Hour)
	c.SetGroupObserver(func(n int) { held = n }, func(_ string, n int) { dry += n })
	c.reconcile(context.Background())
	if g.calls != 1 || held != 2 || dry != 1 {
		t.Fatalf("calls=%d held=%d dry=%d", g.calls, held, dry)
	}
}

// A failing group pass never disturbs the vehicle leases or panics the loop.
func TestCoordinatorGroupFailureIsHarmless(t *testing.T) {
	t.Parallel()
	g := &groupFake{fakeStore: newFakeStore(), err: errors.New("db blip")}
	var acquired int
	c := newCoordinator(g, func(context.Context) ([]string, error) { return []string{"v1"}, nil },
		func(string) { acquired++ }, func(string) {}, nil, time.Hour)
	c.reconcile(context.Background())
	if acquired != 1 || g.calls != 1 {
		t.Fatalf("acquired=%d calls=%d", acquired, g.calls)
	}
}

// Nothing is written once Shutdown has begun.
func TestCoordinatorNoGroupPassWhileClosing(t *testing.T) {
	t.Parallel()
	g := &groupFake{fakeStore: newFakeStore()}
	c := newCoordinator(g, func(context.Context) ([]string, error) { return nil, nil },
		func(string) {}, func(string) {}, nil, time.Hour)
	c.Shutdown(context.Background())
	c.renewGroups(context.Background())
	if g.calls != 0 {
		t.Fatalf("group pass ran while closing: %d", g.calls)
	}
}

// A pod whose group renewals fail past the local deadline (two thirds of the TTL, counted from
// when the last good renew was sent) is counted with source "local"; before that, nothing.
func TestPastLocalDeadline(t *testing.T) {
	t.Parallel()
	s := &Store{ttl: 30 * time.Second, groups: map[string]int64{}}
	if s.PastLocalDeadline() {
		t.Fatal("no group held: nothing to fence")
	}
	s.groups["g"] = 1
	s.groupOKAt = time.Now().Add(-19 * time.Second)
	if s.PastLocalDeadline() {
		t.Error("19 s after the renew was sent is inside the 20 s deadline")
	}
	s.groupOKAt = time.Now().Add(-21 * time.Second)
	if !s.PastLocalDeadline() {
		t.Error("21 s after the renew was sent is past the deadline")
	}
}

type localFake struct {
	groupFake
	past bool
}

func (l *localFake) PastLocalDeadline() bool { return l.past }

func TestCoordinatorCountsLocalDeadline(t *testing.T) {
	t.Parallel()
	f := &localFake{groupFake: groupFake{fakeStore: newFakeStore(), err: errors.New("db blip")}, past: true}
	got := map[string]int{}
	c := newCoordinator(f, func(context.Context) ([]string, error) { return nil, nil },
		func(string) {}, func(string) {}, nil, time.Hour)
	c.SetGroupObserver(func(int) {}, func(source string, n int) { got[source] += n })
	c.reconcile(context.Background())
	if got["local"] != 1 || got["db"] != 0 {
		t.Fatalf("counted %v", got)
	}
	f.past = false
	c.reconcile(context.Background())
	if got["local"] != 1 {
		t.Fatalf("counted inside the deadline: %v", got)
	}
}
