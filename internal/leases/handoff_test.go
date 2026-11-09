package leases

import (
	"context"
	"sync"
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

// The hand-off gap is the wait (release to acquire, the database's clock) plus the acquire to first frame (this pod's clock), is labelled by
// what the vehicle was doing at that frame, and an expired takeover has a reason but no gap.
func TestHandoffGapAndReason(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	frames := map[string]time.Time{}
	kinds := map[string]string{"v1": "drive", "v2": "charge"}
	ps := priorStore{newFakeStore(), map[string]Prior{
		"v1": {Reason: ReasonDrain, Wait: 8 * time.Second, HasWait: true, taken: now},
		"v2": {Reason: ReasonRebalance, Wait: 3 * time.Second, HasWait: true, taken: now},
		"v3": {Reason: ReasonExpired, taken: now},
	}}
	vehicles := []string{"v1", "v2", "v3", "v4"}
	c := newCoordinator(ps, func(context.Context) ([]string, error) { return vehicles, nil }, func(string) {}, func(string) {}, nil, time.Hour)
	c.now = func() time.Time { return now }
	c.SetFrameObserver(func(v string) time.Time { return frames[v] }, func(float64, bool) {})
	reasons := map[string]int{}
	waits := []time.Duration{}
	gaps := map[string]float64{}
	c.SetHandoffObserver(func(v string) string { return kinds[v] }, func(r string, w time.Duration, has bool) {
		reasons[r]++
		if has {
			waits = append(waits, w)
		}
	},
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
	if len(waits) != 2 {
		t.Errorf("waits %v, want the two released groups' waits only", waits)
	}
	if gaps["driving"] != 17 || gaps["charging"] != 12 || len(gaps) != 2 {
		t.Errorf("gaps %v, want driving 17 (8 s wait + 9 s to the frame) and charging 12; none for expired or no hand-off", gaps)
	}
}

// A group that sat free for over ten minutes was not handed off: the store counts it as "other" and gives no wait, so no gap reaches the
// histogram (one such group would wreck the percentile).
func TestPriorOfALongFreeGroupHasNoWait(t *testing.T) {
	s := &Store{priorOf: map[string]Prior{}, vehicleGroup: map[string]string{"v": "g"}}
	s.notePrior("g", false, true, 3*time.Hour, ReasonRebalance)
	p, ok := s.TakePrior("v")
	if !ok || p.Reason != ReasonOther || p.HasWait {
		t.Fatalf("prior %+v ok=%v, want reason other and no wait", p, ok)
	}
	s.notePrior("g", false, true, 9*time.Minute, ReasonRebalance)
	if p, _ := s.TakePrior("v"); p.Reason != ReasonRebalance || !p.HasWait || p.Wait != 9*time.Minute {
		t.Fatalf("prior %+v, want a rebalance with its 9 minute wait", p)
	}
}

// The old owner's stop is reported per vehicle from the stop being asked for to the subscription being gone.
func TestStopObserverReportsEachVehicle(t *testing.T) {
	fs := newFakeStore()
	vehicles := []string{"v1", "v2"}
	streaming := map[string]bool{"v1": true, "v2": true}
	var mu sync.Mutex
	c := newCoordinator(fs, func(context.Context) ([]string, error) { return vehicles, nil },
		func(string) {}, func(v string) {
			go func() { time.Sleep(60 * time.Millisecond); mu.Lock(); streaming[v] = false; mu.Unlock() }()
		}, nil, time.Hour)
	c.SetStreamingCheck(func(v string) bool { mu.Lock(); defer mu.Unlock(); return streaming[v] })
	var got []float64
	c.SetStopObserver(func(s float64) { got = append(got, s) })
	c.reconcile(context.Background())
	c.Shutdown(context.Background())
	if len(got) != 2 || got[0] < 0.05 || got[0] > 2 {
		t.Fatalf("stops %v, want two of about 0.06 s", got)
	}
}
