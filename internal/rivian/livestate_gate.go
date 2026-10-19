package rivian

import (
	"context"
	"sync/atomic"
	"time"
)

// gatedOpTimeout bounds each Redis call made through a GatedLiveStateStore: a
// dead Redis must cost the recorder a fraction of a second, not go-redis's
// 5-second dial timeout on its hot path.
const gatedOpTimeout = 300 * time.Millisecond

// gatedBackoff is how long a gated store leaves Redis alone after it fails.
const gatedBackoff = 30 * time.Second

// GatedLiveStateStore wraps a LiveStateStore for a Redis that may not be
// reachable yet, or any more. While ready() is false, or for gatedBackoff after
// a failed call, it answers as if nothing were stored (Load: not found, writes:
// nil) without touching Redis; otherwise it delegates with a short timeout. This
// is what lets the store be installed at boot before Redis answers (issue #603)
// and start working the moment it does, with the monitors already running.
type GatedLiveStateStore struct {
	inner     LiveStateStore
	ready     func() bool
	now       func() time.Time
	downUntil atomic.Int64 // unix nanos
}

// NewGatedLiveStateStore gates inner behind ready (nil: always ready).
func NewGatedLiveStateStore(inner LiveStateStore, ready func() bool) *GatedLiveStateStore {
	return &GatedLiveStateStore{inner: inner, ready: ready, now: time.Now}
}

func (g *GatedLiveStateStore) usable() bool {
	if g.ready != nil && !g.ready() {
		return false
	}
	return g.now().UnixNano() >= g.downUntil.Load()
}

// call runs fn with a bounded context, and backs off on a failure.
func (g *GatedLiveStateStore) call(ctx context.Context, fn func(ctx context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, gatedOpTimeout)
	defer cancel()
	err := fn(cctx)
	if err != nil {
		g.downUntil.Store(g.now().Add(gatedBackoff).UnixNano())
	}
	return err
}

func (g *GatedLiveStateStore) Save(ctx context.Context, vehicleID string, snap LiveStateSnapshot, ttl time.Duration) error {
	if !g.usable() {
		return nil
	}
	return g.call(ctx, func(c context.Context) error { return g.inner.Save(c, vehicleID, snap, ttl) })
}

func (g *GatedLiveStateStore) Load(ctx context.Context, vehicleID string) (LiveStateSnapshot, bool, error) {
	if !g.usable() {
		return LiveStateSnapshot{}, false, nil
	}
	var snap LiveStateSnapshot
	var ok bool
	err := g.call(ctx, func(c context.Context) error {
		var e error
		snap, ok, e = g.inner.Load(c, vehicleID)
		return e
	})
	if err != nil {
		return LiveStateSnapshot{}, false, err
	}
	return snap, ok, nil
}

func (g *GatedLiveStateStore) Delete(ctx context.Context, vehicleID string) error {
	if !g.usable() {
		return nil
	}
	return g.call(ctx, func(c context.Context) error { return g.inner.Delete(c, vehicleID) })
}

func (g *GatedLiveStateStore) PutState(ctx context.Context, vehicleID string, st *State, ttl time.Duration) error {
	if !g.usable() {
		return nil
	}
	return g.call(ctx, func(c context.Context) error { return g.inner.PutState(c, vehicleID, st, ttl) })
}

func (g *GatedLiveStateStore) GetState(ctx context.Context, vehicleID string) (*State, error) {
	if !g.usable() {
		return nil, nil
	}
	var st *State
	err := g.call(ctx, func(c context.Context) error {
		var e error
		st, e = g.inner.GetState(c, vehicleID)
		return e
	})
	if err != nil {
		return nil, err
	}
	return st, nil
}
