package rivian

import (
	"context"
	"time"
)

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
	// dirty: a periodic write was skipped since the last write, so the stored row
	// is behind the in-memory session. FlushPending writes such sessions.
	dirty bool
}

// due reports whether a periodic write is due at now: the first one always is.
func (g *touchGate) due(now time.Time) bool {
	return g.last.IsZero() || now.Sub(g.last) >= liveTouchInterval
}

// mark records a write (any write: the open, the close and the periodic one).
func (g *touchGate) mark(now time.Time) {
	g.last = now
	g.n++
	g.dirty = false
}

// FlushPending writes every in-progress drive and charge whose periodic write was
// skipped by the throttle, so the stored rows are current. Called on shutdown
// (before the leases are released) and, per vehicle, when a lease is handed off,
// so a normal rollout loses nothing; only a kill -9 can lose up to
// liveTouchInterval of a session's totals. Returns how many rows it wrote.
func (m *StateMonitor) FlushPending(ctx context.Context) int {
	m.sessMu.Lock()
	defer m.sessMu.Unlock()
	n := 0
	for vid := range m.sessions {
		n += m.flushVehicleLocked(ctx, vid)
	}
	return n
}

// flushVehicle is FlushPending for one vehicle.
func (m *StateMonitor) flushVehicle(ctx context.Context, vehicleID string) int {
	m.sessMu.Lock()
	defer m.sessMu.Unlock()
	return m.flushVehicleLocked(ctx, vehicleID)
}

func (m *StateMonitor) flushVehicleLocked(ctx context.Context, vehicleID string) int {
	sess := m.sessions[vehicleID]
	if sess == nil {
		return 0
	}
	n := 0
	if d := sess.drive; d != nil && d.touch.dirty {
		m.upsertLiveDrive(ctx, vehicleID, d)
		n++
	}
	if c := sess.charge; c != nil && c.touch.dirty {
		m.upsertLiveCharge(ctx, vehicleID, c)
		n++
	}
	return n
}

// FlushPending flushes every monitor the registry runs.
func (r *MonitorRegistry) FlushPending(ctx context.Context) int {
	r.mu.RLock()
	mons := make([]*StateMonitor, 0, len(r.monitors))
	for _, e := range r.monitors {
		mons = append(mons, e.monitor)
	}
	r.mu.RUnlock()
	n := 0
	for _, m := range mons {
		if m != nil {
			n += m.FlushPending(ctx)
		}
	}
	return n
}
