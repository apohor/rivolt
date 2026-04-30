package rivian

import (
	"context"
	"testing"
	"time"
)

type capturingObserver struct {
	transitions []string
	trips       []string
}

func (c *capturingObserver) OnStateChange(from, to BreakerState) {
	c.transitions = append(c.transitions, from.String()+"->"+to.String())
}
func (c *capturingObserver) OnTrip(reason string) { c.trips = append(c.trips, reason) }

// fakeClock is a settable monotonic source for breaker tests. We
// can't use time.Now because the breaker's window/cooldown logic
// would be flake-prone with real wall time.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time     { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func newTestBreaker(obs BreakerObserver) (*Breaker, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := NewBreaker(BreakerConfig{
		Window:               time.Minute,
		RateLimitedThreshold: 3,
		OutageThreshold:      8,
		Cooldown:             30 * time.Second,
		MaxCooldown:          5 * time.Minute,
		Now:                  clk.now,
	}, obs)
	return b, clk
}

func TestBreakerTripsOnRateLimited(t *testing.T) {
	t.Parallel()
	obs := &capturingObserver{}
	b, _ := newTestBreaker(obs)

	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("closed breaker should allow: %v", err)
	}
	for i := 0; i < 3; i++ {
		b.Observe(ClassRateLimited)
	}
	if got := b.State(); got != BreakerOpen {
		t.Fatalf("state = %s, want open", got)
	}
	if err := b.Allow(context.Background()); err != ErrUpstreamBreakerOpen {
		t.Fatalf("Allow after trip = %v, want ErrUpstreamBreakerOpen", err)
	}
	if len(obs.trips) != 1 || obs.trips[0] != "rate_limited" {
		t.Fatalf("trips = %v", obs.trips)
	}
}

func TestBreakerWindowEvicts(t *testing.T) {
	t.Parallel()
	obs := &capturingObserver{}
	b, clk := newTestBreaker(obs)

	b.Observe(ClassRateLimited)
	clk.advance(40 * time.Second)
	b.Observe(ClassRateLimited)
	clk.advance(30 * time.Second) // first hit now 70s old, evicted
	b.Observe(ClassRateLimited)   // 2 hits in window, no trip

	if got := b.State(); got != BreakerClosed {
		t.Fatalf("state = %s, want closed (window should evict old hits)", got)
	}
}

func TestBreakerHalfOpenSuccessCloses(t *testing.T) {
	t.Parallel()
	obs := &capturingObserver{}
	b, clk := newTestBreaker(obs)

	for i := 0; i < 3; i++ {
		b.Observe(ClassRateLimited)
	}
	if b.State() != BreakerOpen {
		t.Fatal("expected open")
	}
	// During cooldown: rejected.
	if err := b.Allow(context.Background()); err == nil {
		t.Fatal("expected ErrUpstreamBreakerOpen during cooldown")
	}
	clk.advance(31 * time.Second)
	// First Allow after cooldown admits the probe.
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("Allow probe = %v", err)
	}
	if b.State() != BreakerHalfOpen {
		t.Fatalf("state = %s, want half_open", b.State())
	}
	// Subsequent Allow during half-open is rejected.
	if err := b.Allow(context.Background()); err != ErrUpstreamBreakerOpen {
		t.Fatalf("Allow second-probe = %v", err)
	}
	b.ObserveSuccess()
	if b.State() != BreakerClosed {
		t.Fatalf("state after probe success = %s", b.State())
	}
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("closed breaker should allow: %v", err)
	}
}

func TestBreakerHalfOpenFailureReopensWithLongerCooldown(t *testing.T) {
	t.Parallel()
	obs := &capturingObserver{}
	b, clk := newTestBreaker(obs)

	for i := 0; i < 3; i++ {
		b.Observe(ClassRateLimited)
	}
	clk.advance(31 * time.Second)
	_ = b.Allow(context.Background()) // probe admitted
	b.Observe(ClassRateLimited)       // probe failed
	if b.State() != BreakerOpen {
		t.Fatalf("state after failed probe = %s, want open", b.State())
	}
	// Cooldown should have doubled to 60s; 31s shouldn't be enough.
	clk.advance(31 * time.Second)
	if err := b.Allow(context.Background()); err != ErrUpstreamBreakerOpen {
		t.Fatalf("Allow = %v, want still open with doubled cooldown", err)
	}
	clk.advance(35 * time.Second) // total 66s since reopen
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("Allow after doubled cooldown = %v", err)
	}
}

func TestBreakerTransientDoesNotTrip(t *testing.T) {
	t.Parallel()
	b, _ := newTestBreaker(nil)
	for i := 0; i < 50; i++ {
		b.Observe(ClassTransient)
		b.Observe(ClassUserAction)
		b.Observe(ClassUnknown)
	}
	if b.State() != BreakerClosed {
		t.Fatalf("non-tripping classes should not trip, got %s", b.State())
	}
}

func TestBreakerOutageThreshold(t *testing.T) {
	t.Parallel()
	b, _ := newTestBreaker(nil)
	for i := 0; i < 7; i++ {
		b.Observe(ClassOutage)
	}
	if b.State() != BreakerClosed {
		t.Fatalf("7 outages should not trip (threshold 8), state=%s", b.State())
	}
	b.Observe(ClassOutage)
	if b.State() != BreakerOpen {
		t.Fatalf("8th outage should trip, state=%s", b.State())
	}
}
