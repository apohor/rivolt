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
	c.SetGroupObserver(func(n int) { held = n }, func(n int) { dry += n })
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
