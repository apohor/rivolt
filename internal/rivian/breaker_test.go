package rivian

import (
	"context"
	"errors"
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

func (f *fakeClock) now() time.Time          { return f.t }
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

// trippedAfterCooldown returns a breaker that tripped and whose cooldown has
// elapsed, so the next Allow admits the probe.
func trippedAfterCooldown(t *testing.T) (*Breaker, *fakeClock) {
	t.Helper()
	b, clk := newTestBreaker(nil)
	for i := 0; i < 3; i++ {
		b.Observe(ClassRateLimited)
	}
	if b.State() != BreakerOpen {
		t.Fatal("did not trip")
	}
	clk.advance(31 * time.Second)
	return b, clk
}

// A half-open probe that never reports (it was refused by a later gate, failed
// before the network, or its outcome was never reported) must not wedge the
// breaker: after the probe timeout the next caller is the probe.
func TestHalfOpenProbeHasADeadline(t *testing.T) {
	t.Parallel()
	b, clk := trippedAfterCooldown(t)
	ctx := context.Background()
	if err := b.Allow(ctx); err != nil || b.State() != BreakerHalfOpen {
		t.Fatalf("probe not admitted: %v %s", err, b.State())
	}
	clk.advance(10 * time.Second)
	if err := b.Allow(ctx); err != ErrUpstreamBreakerOpen {
		t.Fatalf("a second caller inside the probe window: %v, want rejected", err)
	}
	clk.advance(21 * time.Second) // 31 s since the probe: past the 30 s timeout
	if err := b.Allow(ctx); err != nil {
		t.Fatalf("the lost probe was never replaced: %v", err)
	}
	// And the new probe is the only one again.
	if err := b.Allow(ctx); err != ErrUpstreamBreakerOpen {
		t.Errorf("two probes at once: %v", err)
	}
	b.ObserveSuccess()
	if b.State() != BreakerClosed {
		t.Errorf("state %s after the probe succeeded", b.State())
	}
}

// A probe that comes back with an error that is neither an outage nor a rate
// limit still has to settle the breaker: an unclassified failure reopens it
// (longer), and a user-action answer (the gateway replied) closes it.
func TestHalfOpenProbeAlwaysSettles(t *testing.T) {
	t.Parallel()
	for _, class := range []ErrorClass{ClassTransient, ClassUnknown} {
		b, _ := trippedAfterCooldown(t)
		_ = b.Allow(context.Background())
		b.Observe(class)
		if b.State() != BreakerOpen {
			t.Errorf("%v: state %s, want open", class, b.State())
		}
		if b.currentCooldown != 60*time.Second {
			t.Errorf("%v: cooldown %v, want doubled", class, b.currentCooldown)
		}
	}
	b, _ := trippedAfterCooldown(t)
	_ = b.Allow(context.Background())
	b.Observe(ClassUserAction)
	if b.State() != BreakerClosed {
		t.Errorf("user action: state %s, want closed", b.State())
	}
	// In the closed state those classes still do nothing.
	c, _ := newTestBreaker(nil)
	c.Observe(ClassTransient)
	c.Observe(ClassUnknown)
	if c.State() != BreakerClosed {
		t.Errorf("closed breaker moved to %s", c.State())
	}
}

type denyLimiter struct{ deny bool }

func (d *denyLimiter) Allow(context.Context, string) (bool, time.Duration) {
	return !d.deny, time.Second
}

// The rate limiter is asked first: a probe the limiter then refuses would never
// reach Rivian and so never report.
func TestRateLimiterRefusalDoesNotSpendTheProbe(t *testing.T) {
	t.Parallel()
	b, _ := trippedAfterCooldown(t)
	rl := &denyLimiter{deny: true}
	c := &LiveClient{breaker: b, rateLimit: rl}
	var rle *ErrRateLimited
	if err := c.checkUpstream(context.Background()); !errors.As(err, &rle) {
		t.Fatalf("limiter refusal: %v", err)
	}
	if b.State() != BreakerOpen {
		t.Fatalf("a refused call touched the breaker: %s", b.State())
	}
	rl.deny = false
	if err := c.checkUpstream(context.Background()); err != nil || b.State() != BreakerHalfOpen {
		t.Fatalf("with a token the probe should go: %v %s", err, b.State())
	}
}

// WebSocket dial and ack outcomes reach the breaker.
func TestWebSocketOutcomesAreReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _ := trippedAfterCooldown(t)
	c := &LiveClient{breaker: b}
	_ = b.Allow(ctx)
	c.reportWS(ctx, nil)
	if b.State() != BreakerClosed {
		t.Errorf("an acknowledged connection left the breaker %s", b.State())
	}
	b, _ = trippedAfterCooldown(t)
	c = &LiveClient{breaker: b}
	_ = b.Allow(ctx)
	c.reportWS(ctx, errors.New("dial tcp: connection refused"))
	if b.State() != BreakerOpen {
		t.Errorf("a failed dial left the breaker %s, want open", b.State())
	}
	// A cancelled context says nothing about Rivian.
	b, _ = trippedAfterCooldown(t)
	c = &LiveClient{breaker: b}
	_ = b.Allow(ctx)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	c.reportWS(cctx, context.Canceled)
	if b.State() != BreakerHalfOpen {
		t.Errorf("a cancelled dial moved the breaker to %s", b.State())
	}
}
