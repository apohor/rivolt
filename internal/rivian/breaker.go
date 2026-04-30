package rivian

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrUpstreamBreakerOpen is returned by the upstream gate when the
// circuit breaker has tripped. It satisfies errors.Is for the same
// "don't bother retrying right now" semantics as ErrUpstreamPaused
// — the api layer maps both to a 503-with-Retry-After.
var ErrUpstreamBreakerOpen = errors.New("rivian upstream breaker open")

// BreakerState describes the breaker's gate behaviour.
//
//	BreakerClosed   — fully open gate, requests flow normally.
//	BreakerOpen     — gate closed, every Allow returns
//	                  ErrUpstreamBreakerOpen until the cooldown
//	                  expires.
//	BreakerHalfOpen — exactly one probe is allowed through; its
//	                  outcome decides whether to close or reopen
//	                  with a doubled cooldown.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerHalfOpen
	BreakerOpen
)

func (s BreakerState) String() string {
	switch s {
	case BreakerHalfOpen:
		return "half_open"
	case BreakerOpen:
		return "open"
	default:
		return "closed"
	}
}

// BreakerConfig tunes the breaker's tripping thresholds.
type BreakerConfig struct {
	// Window is the sliding time window over which failures are
	// counted. Failures older than now-Window are pruned on every
	// Observe call.
	Window time.Duration

	// RateLimitedThreshold is the number of ClassRateLimited
	// outcomes within Window that trips the breaker. 429s are the
	// most expensive failure mode — Rivian's throttle compounds
	// the longer we ignore it — so this is intentionally low.
	RateLimitedThreshold int

	// OutageThreshold is the number of ClassOutage outcomes within
	// Window that trips the breaker. Higher than rate-limited
	// because a single 5xx is often a deploy-window blip we'd
	// rather retry through.
	OutageThreshold int

	// Cooldown is how long the breaker stays open before
	// transitioning to half-open. Doubled (capped at MaxCooldown)
	// if a half-open probe fails.
	Cooldown time.Duration

	// MaxCooldown caps the exponential cooldown growth so a
	// permanently degraded upstream still gets retried regularly
	// instead of asymptotically never.
	MaxCooldown time.Duration

	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// DefaultBreakerConfig returns the production-ready defaults. Tuned
// for a single-region homelab against Rivian's gateway: 60s window,
// trip on 3 rate-limited or 8 outage events, 30s initial cooldown.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		Window:               time.Minute,
		RateLimitedThreshold: 3,
		OutageThreshold:      8,
		Cooldown:             30 * time.Second,
		MaxCooldown:          5 * time.Minute,
	}
}

// BreakerObserver receives state-change notifications. Used to
// drive Prometheus gauges/counters from main.go without coupling
// the breaker to the metrics package.
type BreakerObserver interface {
	OnStateChange(from, to BreakerState)
	OnTrip(reason string) // "rate_limited" or "outage"
}

// Breaker is a sliding-window circuit breaker over Rivian's
// classified upstream errors. Construct one per LiveClient.
//
// Concurrency: every method takes the mutex; the breaker is on the
// hot path of every Rivian call, so contention is bounded by the
// outbound concurrency to Rivian (small — single-digit RPS at the
// scale we're targeting).
type Breaker struct {
	cfg BreakerConfig
	obs BreakerObserver

	mu              sync.Mutex
	state           BreakerState
	rateLimitedHits []time.Time
	outageHits      []time.Time
	openedAt        time.Time
	currentCooldown time.Duration
}

// NewBreaker builds a breaker with the given config. Pass nil
// observer to skip metric/event emission. Zero-valued cfg fields
// are filled with DefaultBreakerConfig values.
func NewBreaker(cfg BreakerConfig, obs BreakerObserver) *Breaker {
	d := DefaultBreakerConfig()
	if cfg.Window <= 0 {
		cfg.Window = d.Window
	}
	if cfg.RateLimitedThreshold <= 0 {
		cfg.RateLimitedThreshold = d.RateLimitedThreshold
	}
	if cfg.OutageThreshold <= 0 {
		cfg.OutageThreshold = d.OutageThreshold
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = d.Cooldown
	}
	if cfg.MaxCooldown <= 0 {
		cfg.MaxCooldown = d.MaxCooldown
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Breaker{cfg: cfg, obs: obs, currentCooldown: cfg.Cooldown}
}

// Allow is the upstream-gate hook. Returns ErrUpstreamBreakerOpen
// when the breaker is open. In half-open state the very first
// caller is admitted as a probe and the rest are rejected; the
// probe's outcome (Observe) decides the next state.
func (b *Breaker) Allow(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case BreakerClosed:
		return nil
	case BreakerOpen:
		if b.cfg.Now().Sub(b.openedAt) < b.currentCooldown {
			return ErrUpstreamBreakerOpen
		}
		// Cooldown elapsed — admit a probe.
		b.transition(BreakerHalfOpen)
		return nil
	case BreakerHalfOpen:
		// We've already admitted the probe; everyone else waits.
		return ErrUpstreamBreakerOpen
	}
	return nil
}

// Observe records the outcome of a Rivian call. Pass the
// ErrorClass returned by the classifier; success-equivalent
// outcomes (ClassUnknown after a 2xx, etc.) should call
// ObserveSuccess instead.
func (b *Breaker) Observe(class ErrorClass) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.cfg.Now()
	cutoff := now.Add(-b.cfg.Window)

	switch class {
	case ClassRateLimited:
		b.rateLimitedHits = pruneBefore(append(b.rateLimitedHits, now), cutoff)
		b.outageHits = pruneBefore(b.outageHits, cutoff)
		if b.state == BreakerHalfOpen {
			b.reopen("rate_limited")
			return
		}
		if b.state == BreakerClosed && len(b.rateLimitedHits) >= b.cfg.RateLimitedThreshold {
			b.trip("rate_limited", now)
		}
	case ClassOutage:
		b.outageHits = pruneBefore(append(b.outageHits, now), cutoff)
		b.rateLimitedHits = pruneBefore(b.rateLimitedHits, cutoff)
		if b.state == BreakerHalfOpen {
			b.reopen("outage")
			return
		}
		if b.state == BreakerClosed && len(b.outageHits) >= b.cfg.OutageThreshold {
			b.trip("outage", now)
		}
	default:
		// Transient / UserAction / Unknown don't count toward the
		// trip thresholds. UserAction is per-user and shouldn't
		// punish the whole pod; transient blips are exactly what
		// the per-call retry loop is for. We still prune so the
		// window stays bounded.
		b.rateLimitedHits = pruneBefore(b.rateLimitedHits, cutoff)
		b.outageHits = pruneBefore(b.outageHits, cutoff)
	}
}

// ObserveSuccess marks a successful call. In half-open state this
// closes the breaker and resets the cooldown.
func (b *Breaker) ObserveSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == BreakerHalfOpen {
		b.transition(BreakerClosed)
		b.currentCooldown = b.cfg.Cooldown
		b.rateLimitedHits = nil
		b.outageHits = nil
	}
}

// State returns the current breaker state. Lock-free read isn't
// worth the complexity at this call frequency; just take the lock.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// trip moves Closed → Open. Caller holds b.mu.
func (b *Breaker) trip(reason string, now time.Time) {
	b.openedAt = now
	b.transition(BreakerOpen)
	if b.obs != nil {
		b.obs.OnTrip(reason)
	}
}

// reopen handles a half-open probe that failed. Caller holds b.mu.
// Doubles the cooldown (capped at MaxCooldown) so a stuck upstream
// doesn't get probed every Cooldown indefinitely.
func (b *Breaker) reopen(reason string) {
	b.openedAt = b.cfg.Now()
	b.currentCooldown *= 2
	if b.currentCooldown > b.cfg.MaxCooldown {
		b.currentCooldown = b.cfg.MaxCooldown
	}
	b.transition(BreakerOpen)
	if b.obs != nil {
		b.obs.OnTrip(reason)
	}
}

// transition is a state-change helper that fires the observer
// exactly once per real change. Caller holds b.mu.
func (b *Breaker) transition(to BreakerState) {
	if b.state == to {
		return
	}
	from := b.state
	b.state = to
	if b.obs != nil {
		b.obs.OnStateChange(from, to)
	}
}

// pruneBefore drops entries older than cutoff. Times are appended
// in monotonic order so we can stop at the first kept entry.
func pruneBefore(ts []time.Time, cutoff time.Time) []time.Time {
	for i, t := range ts {
		if t.After(cutoff) || t.Equal(cutoff) {
			if i == 0 {
				return ts
			}
			out := make([]time.Time, len(ts)-i)
			copy(out, ts[i:])
			return out
		}
	}
	return nil
}
