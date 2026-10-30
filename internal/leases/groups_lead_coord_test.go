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
