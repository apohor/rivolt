package leases

import (
	"context"
	"reflect"
	"testing"
)

func activityOf(m map[string]Activity) func(string) Activity {
	return func(vid string) Activity { return m[vid] }
}

// Over the share, the shed never takes a group with an open drive or charge and prefers a sleeping
// group over an awake one, whatever the store's default order says.
func TestLeadCoordinator_ShedPrefersIdleAndNeverBusy(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1", "v2": "g2", "v3": "g3"},
		res: GroupLead{Held: map[string]int64{"g1": 1, "g2": 1, "g3": 1}}}
	c := newLeadCoord(ls, []string{"v1", "v2", "v3"})
	var choices []string
	c.SetActivityCheck(activityOf(map[string]Activity{"v1": ActivityIdle, "v2": ActivityAwake, "v3": ActivityBusy}),
		func(ch string) { choices = append(choices, ch) })
	c.reconcile(context.Background())
	ls.lmu.Lock()
	ls.log = nil
	ls.res.Shed, ls.res.ShedOrder = []string{"g3"}, []string{"g3", "g2", "g1"}
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	if got, want := ls.calls(), []string{"stop:v1", "release:v1", "releaseGroup:g1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v (the sleeping group, not the store's busy default)", got, want)
	}
	if !reflect.DeepEqual(choices, []string{"idle"}) {
		t.Errorf("shed choices %v, want [idle]", choices)
	}

	// No sleeping group left over the share: the awake one, still never the busy one.
	ls.lmu.Lock()
	ls.log = nil
	ls.res.Held = map[string]int64{"g2": 1, "g3": 1}
	ls.res.Shed, ls.res.ShedOrder = []string{"g3"}, []string{"g3", "g2"}
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	if got, want := ls.calls(), []string{"stop:v2", "release:v2", "releaseGroup:g2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// Every group over the share busy: nothing is shed that pass, and it is counted as deferred.
func TestLeadCoordinator_ShedDeferredWhileEveryGroupIsBusy(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1", "v2": "g2"},
		res: GroupLead{Held: map[string]int64{"g1": 1, "g2": 1}}}
	c := newLeadCoord(ls, []string{"v1", "v2"})
	var choices []string
	c.SetActivityCheck(activityOf(map[string]Activity{"v1": ActivityBusy, "v2": ActivityBusy}),
		func(ch string) { choices = append(choices, ch) })
	c.reconcile(context.Background())
	ls.lmu.Lock()
	ls.log = nil
	ls.res.Shed, ls.res.ShedOrder = []string{"g2"}, []string{"g2", "g1"}
	ls.lmu.Unlock()
	c.reconcile(context.Background())
	if got := ls.calls(); len(got) != 0 {
		t.Fatalf("calls = %v, want none while both groups have an open session", got)
	}
	if len(c.owned) != 2 {
		t.Errorf("owned %v, want both", c.owned)
	}
	if !reflect.DeepEqual(choices, []string{"deferred"}) {
		t.Errorf("shed choices %v, want [deferred]", choices)
	}
}

// acquirerStore records AcquireGroups and grants what is in grant.
type acquirerStore struct {
	*leaderStore
	grant    map[string]int64
	acquired [][]string
}

func (a *acquirerStore) AcquireGroups(_ context.Context, groups []string) (map[string]int64, error) {
	a.acquired = append(a.acquired, append([]string(nil), groups...))
	out := map[string]int64{}
	for _, g := range groups {
		if e, ok := a.grant[g]; ok {
			out[g] = e
		}
	}
	return out, nil
}

// A vehicle with an open session whose group changed under it (re-keyed by a shared vehicle) takes
// its new group along instead of stopping; an idle one stops as before, and so does a busy one
// whose new group cannot be taken.
func TestLeadCoordinator_OpenSessionFollowsItsGroup(t *testing.T) {
	for _, c := range []struct {
		name      string
		act       Activity
		grant     map[string]int64
		wantAsk   bool
		wantCalls []string
	}{
		{"busy, group free", ActivityBusy, map[string]int64{"g9": 7}, true, []string{"releaseGroup:g1"}},
		{"busy, group taken elsewhere", ActivityBusy, nil, true, []string{"stop:v1", "release:v1", "releaseGroup:g1"}},
		{"idle", ActivityIdle, map[string]int64{"g9": 7}, false, []string{"stop:v1", "release:v1", "releaseGroup:g1"}},
	} {
		ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"},
			res: GroupLead{Held: map[string]int64{"g1": 1}}}
		as := &acquirerStore{leaderStore: ls, grant: c.grant}
		co := newCoordinator(as, func(context.Context) ([]string, error) { return []string{"v1"}, nil },
			func(string) {}, func(v string) { ls.note("stop:" + v) }, nil, 0)
		co.SetActivityCheck(activityOf(map[string]Activity{"v1": c.act}), nil)
		co.reconcile(context.Background())
		if _, ok := co.owned["v1"]; !ok {
			t.Fatalf("%s: v1 not owned after the first pass", c.name)
		}
		// The re-key: v1 now maps to g9; g1 is no longer a candidate and is retired.
		ls.lmu.Lock()
		ls.log = nil
		ls.vg = map[string]string{"v1": "g9"}
		ls.res = GroupLead{Held: map[string]int64{}, Retire: []string{"g1"}, Lost: []string{"g1"}}
		ls.lmu.Unlock()
		co.reconcile(context.Background())
		if asked := len(as.acquired) > 0; asked != c.wantAsk || (asked && !reflect.DeepEqual(as.acquired[0], []string{"g9"})) {
			t.Errorf("%s: AcquireGroups calls %v, want asked=%v for [g9]", c.name, as.acquired, c.wantAsk)
		}
		if got := ls.calls(); !reflect.DeepEqual(got, c.wantCalls) {
			t.Errorf("%s: calls = %v, want %v", c.name, got, c.wantCalls)
		}
	}
}
