package rivian

import "time"

// liveTouchInterval is how often a drive or charge in progress is re-upserted. The
// periodic write ran on every frame (every 1 to 5 s), rewriting the whole row,
// polyline included, so a long drive wrote a growing text column O(n^2) times.
// Opening, closing, merging and stale-closing a session still write at once; this
// only throttles the "still going" write. The cost is that a crash loses at most
// this much of a session's running totals (the Redis live-state snapshot covers
// hand-offs).
const liveTouchInterval = 10 * time.Second

// liveTouchNow is the clock the throttle reads. A variable so tests can drive it.
var liveTouchNow = time.Now

// touchGate remembers when a session was last written.
type touchGate struct {
	last time.Time
	n    int // writes marked, for tests
}

// due reports whether a periodic write is due at now: the first one always is.
func (g *touchGate) due(now time.Time) bool {
	return g.last.IsZero() || now.Sub(g.last) >= liveTouchInterval
}

// mark records a write (any write: the open, the close and the periodic one).
func (g *touchGate) mark(now time.Time) {
	g.last = now
	g.n++
}
