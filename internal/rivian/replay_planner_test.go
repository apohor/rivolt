package rivian

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubPlanner struct {
	plan  *TripPlan
	err   error
	calls int
}

func (s *stubPlanner) PlanTrip(context.Context, PlanTripInput) (*TripPlan, error) {
	s.calls++
	return s.plan, s.err
}

func TestFirstPlannerFallsThroughOnlyOnNoFixture(t *testing.T) {
	miss := &stubPlanner{err: ErrNoFixture}
	hit := &stubPlanner{plan: &TripPlan{Status: "OK", Simulated: true}}
	boom := &stubPlanner{err: errors.New("router down")}
	after := &stubPlanner{plan: &TripPlan{Status: "OK"}}

	if got, err := (FirstPlanner{miss, hit, after}).PlanTrip(context.Background(), PlanTripInput{}); err != nil || got != hit.plan || after.calls != 0 {
		t.Errorf("miss then hit = %v, %v (later planners called: %d)", got, err, after.calls)
	}
	if _, err := (FirstPlanner{boom, after}).PlanTrip(context.Background(), PlanTripInput{}); err == nil || errors.Is(err, ErrNoFixture) || after.calls != 0 {
		t.Errorf("a real error must end the search: %v (later planners called: %d)", err, after.calls)
	}
	if _, err := (FirstPlanner{miss, nil, miss}).PlanTrip(context.Background(), PlanTripInput{}); !errors.Is(err, ErrNoFixture) {
		t.Errorf("all missed = %v, want ErrNoFixture", err)
	}
	if _, err := (FirstPlanner{}).PlanTrip(context.Background(), PlanTripInput{}); !errors.Is(err, ErrNoFixture) {
		t.Errorf("empty = %v, want ErrNoFixture", err)
	}
}

func TestReplayClientPlansThroughItsPlanner(t *testing.T) {
	want := &TripPlan{Status: "OK", Simulated: true}
	c := &ReplayClient{planner: &stubPlanner{plan: want}}
	if got, err := c.PlanTrip(context.Background(), PlanTripInput{}); err != nil || got != want {
		t.Errorf("PlanTrip = %v, %v", got, err)
	}
	if _, err := (&ReplayClient{}).PlanTrip(context.Background(), PlanTripInput{}); !errors.Is(err, ErrNoPlanner) {
		t.Errorf("no planner = %v, want ErrNoPlanner", err)
	}
}

func TestReplayRegistryGivesEveryUserThePlanner(t *testing.T) {
	feed, _ := testFeed(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC), time.Hour)
	want := &TripPlan{Status: "OK", Simulated: true}
	reg := NewReplayAccountRegistry(feed, &stubPlanner{plan: want})
	for i := 0; i < 2; i++ {
		tp, ok := reg.For(uuid.New()).(TripPlanner)
		if !ok {
			t.Fatal("a replay account must be a TripPlanner")
		}
		if got, err := tp.PlanTrip(context.Background(), PlanTripInput{}); err != nil || got != want {
			t.Errorf("user %d: %v, %v", i, got, err)
		}
	}
	// Registered without a planner, the account refuses cleanly.
	bare := NewReplayAccountRegistry(feed, nil)
	if _, err := bare.For(uuid.New()).(TripPlanner).PlanTrip(context.Background(), PlanTripInput{}); !errors.Is(err, ErrNoPlanner) {
		t.Errorf("no planner = %v", err)
	}
}
