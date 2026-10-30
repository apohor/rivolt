package leases

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// leaderStore is a fakeStore that also leads with group leases, with a call log.
type leaderStore struct {
	*fakeStore
	lmu        sync.Mutex
	log        []string
	res        GroupLead
	leadErr    error
	vg         map[string]string
	releaseErr map[string]error
}

func (l *leaderStore) note(s string) {
	l.lmu.Lock()
	l.log = append(l.log, s)
	l.lmu.Unlock()
}
func (l *leaderStore) calls() []string {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	return append([]string(nil), l.log...)
}
func (l *leaderStore) GroupLeading() bool { return true }
func (l *leaderStore) LeadGroups(context.Context) (GroupLead, error) {
	if l.leadErr != nil {
		return GroupLead{}, l.leadErr
	}
	held := map[string]int64{}
	for g, e := range l.res.Held {
		held[g] = e
	}
	r := l.res
	r.Held = held
	return r, nil
}
func (l *leaderStore) ReleaseGroup(_ context.Context, g string) error {
	l.note("releaseGroup:" + g)
	return nil
}
func (l *leaderStore) GroupVehicles(context.Context) (map[string]string, error) { return l.vg, nil }
func (l *leaderStore) Release(ctx context.Context, vid string) error {
	l.note("release:" + vid)
	if err := l.releaseErr[vid]; err != nil {
		return err
	}
	return l.fakeStore.Release(ctx, vid)
}

func newLeadCoord(ls *leaderStore, vehicles []string) *Coordinator {
	return newCoordinator(ls,
		func(context.Context) ([]string, error) { return vehicles, nil },
		func(string) {},
		func(v string) { ls.note("stop:" + v) },
		nil, time.Hour)
}

func TestLeadCoordinator_ShedStopsThenReleasesVehicleThenGroup(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1", "v2": "g2"},
		res: GroupLead{Held: map[string]int64{"g1": 1, "g2": 1}}}
	c := newLeadCoord(ls, []string{"v1", "v2"})
	c.reconcile(context.Background())
	if len(c.owned) != 2 {
		t.Fatalf("owned %v", c.owned)
	}
	ls.lmu.Lock()
	ls.log = nil
	ls.res.Shed = []string{"g2"}
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	want := []string{"stop:v2", "release:v2", "releaseGroup:g2"}
	if got := ls.calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if _, ok := c.owned["v2"]; ok {
		t.Error("v2 still owned")
	}
}

func TestLeadCoordinator_FailedVehicleReleaseKeepsTheGroup(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"},
		res: GroupLead{Held: map[string]int64{"g1": 1}}}
	c := newLeadCoord(ls, []string{"v1"})
	c.reconcile(context.Background())
	ls.lmu.Lock()
	ls.log = nil
	ls.res.Shed = []string{"g1"}
	ls.releaseErr = map[string]error{"v1": errors.New("db")}
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	for _, s := range ls.calls() {
		if s == "releaseGroup:g1" {
			t.Fatalf("group released while its vehicle lease is still held: %v", ls.calls())
		}
	}
	if _, ok := c.owned["v1"]; !ok {
		t.Error("v1 should still be owned (release failed)")
	}
}

func TestLeadCoordinator_OwnedVehicleOfAGroupNotHeldIsReleased(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"},
		res: GroupLead{Held: map[string]int64{"g1": 1}}}
	c := newLeadCoord(ls, []string{"v1"})
	c.reconcile(context.Background())
	// The flag flip, or drift: this pod owns v1 but no longer holds g1.
	ls.lmu.Lock()
	ls.log = nil
	ls.res = GroupLead{Held: map[string]int64{}}
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	if got, want := ls.calls(), []string{"stop:v1", "release:v1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	c.reconcile(context.Background())
	if _, ok := ls.fakeStore.owned["v1"]; ok {
		t.Error("v1 re-acquired or renewed")
	}
}

func TestLeadCoordinator_FailedLeadPassAcquiresAndReleasesNothing(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"},
		res: GroupLead{Held: map[string]int64{"g1": 1}}}
	c := newLeadCoord(ls, []string{"v1"})
	c.reconcile(context.Background())
	ls.lmu.Lock()
	ls.log = nil
	ls.leadErr = errors.New("db down")
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	if got := ls.calls(); len(got) != 0 {
		t.Fatalf("calls after a failed pass: %v", got)
	}
	if _, ok := c.owned["v1"]; !ok {
		t.Error("a failed pass dropped v1")
	}
}

type drainStore struct {
	*leaderStore
}

func (d *drainStore) MarkDraining(context.Context) error { d.note("markDraining"); return nil }

func TestShutdownMarksDrainingFirst(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"}, res: GroupLead{Held: map[string]int64{"g1": 1}}}
	ds := &drainStore{ls}
	c := newCoordinator(ds, func(context.Context) ([]string, error) { return []string{"v1"}, nil },
		func(string) {}, func(v string) { ls.note("stop:" + v) }, nil, time.Hour)
	c.reconcile(context.Background())
	ls.lmu.Lock()
	ls.log = nil
	ls.lmu.Unlock()
	c.Shutdown(context.Background())
	got := ls.calls()
	if len(got) < 2 || got[0] != "markDraining" || got[1] != "stop:v1" {
		t.Fatalf("calls = %v, want markDraining then stop:v1", got)
	}
	// And a pass after the drain began does nothing.
	c.reconcile(context.Background())
	if n := len(ls.calls()); n != len(got) {
		t.Errorf("reconcile after Shutdown made calls: %v", ls.calls()[len(got):])
	}
}

type fencingStore struct {
	*leaderStore
	past    bool
	dropped bool
}

func (f *fencingStore) PastLocalDeadline() bool { return f.past }
func (f *fencingStore) DropGroups()             { f.dropped = true; f.past = false }

func TestLeadCoordinator_SelfFencesPastTheLocalDeadline(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1", "v2": "g2"},
		res: GroupLead{Held: map[string]int64{"g1": 1, "g2": 1}}}
	fs := &fencingStore{leaderStore: ls}
	c := newCoordinator(fs, func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(string) {}, func(v string) { ls.note("stop:" + v) }, nil, time.Hour)
	fences := 0
	c.SetSelfFenceObserver(func() { fences++ })
	c.reconcile(context.Background())
	if len(c.owned) != 2 {
		t.Fatalf("owned %v", c.owned)
	}
	// Renewals fail, but not for long enough: nothing stops.
	ls.leadErr = errors.New("db slow")
	c.reconcile(context.Background())
	if len(c.owned) != 2 || fences != 0 {
		t.Fatalf("stopped before the local deadline: owned=%d fences=%d", len(c.owned), fences)
	}
	// Past the local deadline: every subscription stops, the tokens are dropped, once.
	fs.past = true
	c.reconcile(context.Background())
	if len(c.owned) != 0 || fences != 1 || !fs.dropped {
		t.Fatalf("owned=%d fences=%d dropped=%v, want 0/1/true", len(c.owned), fences, fs.dropped)
	}
	c.reconcile(context.Background())
	if fences != 1 {
		t.Errorf("self-fenced again: %d", fences)
	}
}

func TestNextTickJitterStaysInTheBand(t *testing.T) {
	c := newCoordinator(newFakeStore(), nil, func(string) {}, func(string) {}, nil, groupTick)
	if c.nextTick() != groupTick {
		t.Fatal("no jitter configured, the tick should be exact")
	}
	c.jitter = groupTickJitter
	lo, hi := groupTick, groupTick
	for i := 0; i < 2000; i++ {
		d := c.nextTick()
		if d < groupTick-groupTickJitter || d >= groupTick+groupTickJitter {
			t.Fatalf("tick %v outside 5 s +- 1 s", d)
		}
		if d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	if hi-lo < time.Second {
		t.Errorf("ticks vary only %v: no jitter", hi-lo)
	}
}
