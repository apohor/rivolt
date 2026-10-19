package rivian

import (
	"context"
	"sync"
	"time"
)

// defaultStaggerInterval spaces first connects 50 ms apart: 20 a second, so a
// rollout of 1000 vehicles takes under a minute and never bursts.
const defaultStaggerInterval = 50 * time.Millisecond

// Stagger hands out time slots at least interval apart. The monitors share one
// (MonitorRegistry.SetStagger), so the first connect of every subscription in the
// process is spaced, not only the ones of a single user. A nil or zero-interval
// Stagger never waits.
type Stagger struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

// NewStagger returns a Stagger with the given spacing (<= 0: none).
func NewStagger(interval time.Duration) *Stagger { return &Stagger{interval: interval} }

// Wait blocks until the caller's slot, or ctx is done. Slots are reserved under
// the lock and slept outside it, so callers queue up in order.
func (s *Stagger) Wait(ctx context.Context) {
	if s == nil || s.interval <= 0 {
		return
	}
	s.mu.Lock()
	now := time.Now()
	next := s.last.Add(s.interval)
	if now.Before(next) {
		s.last = next
		wait := next.Sub(now)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		return
	}
	s.last = now
	s.mu.Unlock()
}
