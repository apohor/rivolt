package leases

import (
	"context"
	"testing"
	"time"
)

// priorStore is the fake store plus the lead-mode Store's memory of how each group came free.
type priorStore struct {
	*fakeStore
	prior map[string]Prior
}

func (p priorStore) TakePrior(v string) (Prior, bool) {
	pr, ok := p.prior[v]
	delete(p.prior, v)
	return pr, ok
}

// The hand-off gap runs from the previous holder's release to the first frame here (not from this pod's acquire), is labelled by what the
// vehicle was doing at that frame, and an expired takeover has a reason but no gap.
func TestHandoffGapAndReason(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	frames := map[string]time.Time{}
	kinds := map[string]string{"v1": "drive", "v2": "charge"}
	ps := priorStore{newFakeStore(), map[string]Prior{
		"v1": {Reason: ReasonDrain, Released: now.Add(-8 * time.Second), taken: now},
		"v2": {Reason: ReasonRebalance, Released: now.Add(-3 * time.Second), taken: now},
		"v3": {Reason: ReasonExpired, taken: now},
	}}
	vehicles := []string{"v1", "v2", "v3", "v4"}
	c := newCoordinator(ps, func(context.Context) ([]string, error) { return vehicles, nil }, func(string) {}, func(string) {}, nil, time.Hour)
	c.now = func() time.Time { return now }
	c.SetFrameObserver(func(v string) time.Time { return frames[v] }, func(float64, bool) {})
	reasons := map[string]int{}
	gaps := map[string]float64{}
	c.SetHandoffObserver(func(v string) string { return kinds[v] }, func(r string) { reasons[r]++ },
		func(s float64, state string) { gaps[state] = s })

	c.reconcile(context.Background())
	for _, v := range vehicles {
		frames[v] = now.Add(9 * time.Second)
	}
	now = now.Add(10 * time.Second)
	c.reconcile(context.Background())

	if reasons[ReasonDrain] != 1 || reasons[ReasonRebalance] != 1 || reasons[ReasonExpired] != 1 || len(reasons) != 3 {
		t.Errorf("reasons %v, want one each of drain, rebalance, expired (v4 was no hand-off)", reasons)
	}
	if gaps["driving"] != 17 || gaps["charging"] != 12 || len(gaps) != 2 {
		t.Errorf("gaps %v, want driving 17 (8 s release lead + 9 s) and charging 12; none for expired or no hand-off", gaps)
	}
}
