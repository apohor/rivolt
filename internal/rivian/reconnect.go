package rivian

import (
	"math/rand"
	"time"
)

// Reconnect pacing for the WebSocket subscriptions.
//
// Rivian closes every subscription connection about every 3 minutes
// (close code 4420, "Connection TTL expired"), so each Subscribe* loop
// reconnects ~20 times an hour. Each loop used to keep one attempt
// counter for its whole lifetime, never reset, feeding 1<<attempt
// seconds of backoff. A healthy pod's reconnect gap therefore drifted
// with uptime: growing waits for the first ~50 min, then 5 minutes
// down in every 8 until ~4 h, then (once the shift overflowed int64)
// reconnecting within a second forever. Subscription coverage, and
// with it how often the cloud was asked about a sleeping car, depended
// on how long ago the pod was deployed.
//
// reconnectBackoff resets after a healthy session, so a TTL close is
// followed by a ~1 s reconnect at any uptime (what long-running prod
// pods already did), and caps the exponent so it can't overflow.
// Sessions that fail fast still back off 1 s → 2 s → … → 5 min.

const (
	// healthySession: a session that lasted this long wasn't a fast
	// failure, so the next reconnect starts the backoff from scratch.
	healthySession   = 30 * time.Second
	maxReconnectWait = 5 * time.Minute
	// maxAttempt keeps 1<<attempt seconds well inside int64 and above
	// maxReconnectWait (2^9 s = 8.5 min).
	maxAttempt = 9
)

type reconnectBackoff struct {
	attempt int
	started time.Time
	now     func() time.Time // tests
}

func (b *reconnectBackoff) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// start marks the beginning of a session.
func (b *reconnectBackoff) start() { b.started = b.clock() }

// next returns how long to wait before reconnecting after the session
// that began at the last start.
func (b *reconnectBackoff) next() time.Duration {
	if !b.started.IsZero() && b.clock().Sub(b.started) >= healthySession {
		b.attempt = 0
	}
	wait := time.Duration(1<<b.attempt)*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond
	if wait > maxReconnectWait {
		wait = maxReconnectWait
	}
	if b.attempt < maxAttempt {
		b.attempt++
	}
	return wait
}
