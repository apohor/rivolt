package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
)

// leg is a fake planner leg: what the cache layer does with it decides whether the timing wrapper (which sits inside the cache) is reached.
type fakeLegPlanner struct {
	inner *timedPlanner
	mode  string // "hit" | "miss" | "quota" | "miss-error"
}

func (f *fakeLegPlanner) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	switch f.mode {
	case "hit":
		return &rivian.TripPlan{}, nil
	case "quota":
		return nil, &planleg.QuotaError{RetryAfter: time.Minute}
	}
	return f.inner.PlanTrip(ctx, in)
}

type slowPlanner struct {
	err   error
	sleep time.Duration
}

func (p slowPlanner) PlanTrip(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
	time.Sleep(p.sleep)
	return &rivian.TripPlan{}, p.err
}

// Every leg is counted by what became of it, and only the legs that reached the planner are timed.
func TestPlannerLegsAreCountedAndOnlyMissesAreTimed(t *testing.T) {
	m := metrics.New()
	timed := &timedPlanner{inner: slowPlanner{sleep: 2 * time.Millisecond}, m: m}
	timedErr := &timedPlanner{inner: slowPlanner{err: errors.New("gateway down")}, m: m}
	for _, c := range []struct {
		mode string
		in   *timedPlanner
	}{{"hit", timed}, {"hit", timed}, {"miss", timed}, {"quota", timed}, {"miss", timedErr}} {
		cp := &countedPlanner{inner: &fakeLegPlanner{inner: c.in, mode: c.mode}, m: m}
		_, _ = cp.PlanTrip(context.Background(), rivian.PlanTripInput{})
	}
	for result, want := range map[string]float64{"hit": 2, "miss": 1, "quota": 1, "error": 1} {
		if got := testutil.ToFloat64(m.TripPlanLegsTotal.WithLabelValues(result)); got != want {
			t.Errorf("legs{%s} = %v, want %v", result, got, want)
		}
	}
	if n := testutil.CollectAndCount(m.TripPlanUpstreamSeconds); n != 2 {
		t.Errorf("upstream histogram has %d series, want 2 (ok and error): hits and quota refusals must not be timed", n)
	}
}

// A nil metrics (a test or an install without them) changes nothing.
func TestPlannerWrappersWorkWithoutMetrics(t *testing.T) {
	cp := &countedPlanner{inner: &timedPlanner{inner: slowPlanner{}}}
	if _, err := cp.PlanTrip(context.Background(), rivian.PlanTripInput{}); err != nil {
		t.Fatal(err)
	}
}
