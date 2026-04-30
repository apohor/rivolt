// Package leases coordinates per-vehicle ownership across multiple
// rivolt pods so each vehicle is subscribed by exactly one process.
//
// Mechanism (see migration 0011):
//
//   - The `subscription_leases` table pins a vehicle to a pod_id.
//   - Acquire() inserts a row, or "steals" one whose expires_at is
//     in the past via INSERT ... ON CONFLICT DO UPDATE WHERE.
//   - Renew() bumps renewed_at + expires_at on rows we own.
//   - ReleaseAll() deletes our rows on SIGTERM so a planned restart
//     hands ownership over without waiting for the TTL.
//
// Coordinator is the run loop that ties Store to a callback surface:
// "tell me when I gain a vehicle, tell me when I lose one." The
// state-monitor goroutine wires those callbacks to EnsureSubscribed
// / Unsubscribe so the WS subscription set tracks lease ownership.
//
// Trade-offs:
//   - Acquisition is opportunistic — every pod tries every vehicle
//     each cycle. At 1000 vehicles / 3-8 pods that's a few hundred
//     INSERTs every 30s, which Postgres eats trivially. Consistent-
//     hashing the vehicle set across pods is a future optimization
//     when the storm becomes visible in pg_stat_statements.
//   - The TTL has to be long enough that a pod under load doesn't
//     lose its leases mid-renew, and short enough that a crashed pod's
//     vehicles aren't stale for too long. 2 minutes is the
//     architecture-doc default; renew every 30s gives 4x headroom.
package leases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultTTL is the lease lifetime stamped on each Acquire/Renew.
// Pods renew every TTL/4 — see Coordinator.reconcileInterval.
const DefaultTTL = 2 * time.Minute

// Store is the data-access layer for the subscription_leases table.
// All methods are safe to call concurrently.
type Store struct {
	db    *sql.DB
	podID string
	ttl   time.Duration
}

// NewStore wraps a *sql.DB and pins the calling pod's identity. The
// pod_id should be stable for the process lifetime — typically the
// k8s pod name via the downward API. An empty pod_id is a
// configuration bug; NewStore returns an error rather than letting
// every pod share an identity (which would silently disable the
// whole coordination layer).
func NewStore(db *sql.DB, podID string) (*Store, error) {
	if db == nil {
		return nil, errors.New("leases: nil *sql.DB")
	}
	if podID == "" {
		return nil, errors.New("leases: empty pod_id (set RIVOLT_POD_ID via downward API)")
	}
	return &Store{db: db, podID: podID, ttl: DefaultTTL}, nil
}

// PodID returns the identity this Store acquires leases under.
func (s *Store) PodID() string { return s.podID }

// Acquire attempts to claim ownership of vehicleID. Returns true if
// this pod now owns the lease (newly inserted OR successfully stolen
// from an expired holder), false if a healthy lease held by another
// pod blocked the claim.
//
// The single SQL statement does both insert-and-steal in one atomic
// step so two pods racing on the same expired lease both can't win:
// Postgres' row-level lock during ON CONFLICT serializes them.
func (s *Store) Acquire(ctx context.Context, vehicleID string) (bool, error) {
	const q = `
        INSERT INTO subscription_leases (vehicle_id, pod_id, acquired_at, renewed_at, expires_at)
        VALUES ($1, $2, now(), now(), now() + $3::interval)
        ON CONFLICT (vehicle_id) DO UPDATE
            SET pod_id      = EXCLUDED.pod_id,
                acquired_at = EXCLUDED.acquired_at,
                renewed_at  = EXCLUDED.renewed_at,
                expires_at  = EXCLUDED.expires_at
            -- Only steal when (a) the existing lease has expired or
            -- (b) we already own it (idempotent re-acquire). The
            -- second clause makes Acquire safe to retry without
            -- racing the renew loop.
            WHERE subscription_leases.expires_at < now()
               OR subscription_leases.pod_id = EXCLUDED.pod_id
        RETURNING pod_id`

	var winner string
	err := s.db.QueryRowContext(ctx, q, vehicleID, s.podID, fmt.Sprintf("%d seconds", int(s.ttl.Seconds()))).Scan(&winner)
	if errors.Is(err, sql.ErrNoRows) {
		// ON CONFLICT predicate didn't match — a healthy lease held
		// by some other pod. Not our error, just not our turn.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire %s: %w", vehicleID, err)
	}
	return winner == s.podID, nil
}

// Renew extends every lease this pod currently owns. Returns the
// vehicleIDs that are still owned after the renew (i.e. weren't
// stolen out from under us between the previous reconcile and now).
//
// A vehicle missing from the result set is one we lost ownership
// of — the caller (Coordinator) translates that into an Unsubscribe
// callback so the WS subscription is torn down promptly.
func (s *Store) Renew(ctx context.Context) ([]string, error) {
	const q = `
        UPDATE subscription_leases
           SET renewed_at = now(),
               expires_at = now() + $2::interval
         WHERE pod_id = $1
        RETURNING vehicle_id`

	rows, err := s.db.QueryContext(ctx, q, s.podID, fmt.Sprintf("%d seconds", int(s.ttl.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("renew: %w", err)
	}
	defer rows.Close()

	var owned []string
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			return nil, fmt.Errorf("renew scan: %w", err)
		}
		owned = append(owned, vid)
	}
	return owned, rows.Err()
}

// Release drops a single lease. Used when a vehicle disappears from
// /api/vehicles (e.g. the user removed it) so we don't keep
// pretending to own a no-longer-existing subscription.
func (s *Store) Release(ctx context.Context, vehicleID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM subscription_leases WHERE vehicle_id = $1 AND pod_id = $2`,
		vehicleID, s.podID,
	)
	if err != nil {
		return fmt.Errorf("release %s: %w", vehicleID, err)
	}
	return nil
}

// ReleaseAll drops every lease owned by this pod. Called from the
// SIGTERM hook so a graceful shutdown hands ownership to surviving
// pods on their NEXT reconcile cycle (no waiting for the TTL).
func (s *Store) ReleaseAll(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM subscription_leases WHERE pod_id = $1`,
		s.podID,
	)
	if err != nil {
		return 0, fmt.Errorf("release-all: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Coordinator runs the lease reconciliation loop and bridges
// Acquire/lose events to subscription start/stop callbacks.
//
// Lifecycle:
//
//	c := NewCoordinator(store, vehicles, onAcquire, onRelease, logger)
//	go c.Run(ctx)             // ticks every reconcileInterval
//	defer c.Shutdown(ctx)     // calls ReleaseAll
type Coordinator struct {
	store storeAPI
	log   *slog.Logger

	// vehicles returns the full set of vehicleIDs the cluster cares
	// about. Source of truth varies — usually a "list every distinct
	// vehicle_id from the vehicles table" closure. Returning a fresh
	// slice each call is fine; reconcile makes its own copy.
	vehicles func(ctx context.Context) ([]string, error)

	// onAcquire is fired when reconciliation transitions a vehicle
	// from unowned-by-us to owned-by-us. onRelease fires the inverse.
	// Both run synchronously inside the reconcile loop; long-running
	// work should hand off to a goroutine.
	onAcquire func(vehicleID string)
	onRelease func(vehicleID string)

	// onCountChange fires after every reconcile with the current
	// owned-lease count. The metrics package wires this to the
	// rivolt_subscription_leases gauge. Optional.
	onCountChange func(count int)

	reconcileInterval time.Duration

	// trigger lets external callers (e.g. the StateMonitor's
	// post-RefreshVehicleInfo goroutine) ask the coordinator to
	// reconcile immediately rather than wait for the next tick.
	// Buffered cap-1 with non-blocking send: many simultaneous
	// pokes coalesce into a single reconcile.
	trigger chan struct{}

	mu    sync.Mutex
	owned map[string]struct{}
}

// storeAPI is the slice of *Store the Coordinator actually uses.
// Pulled out so tests can supply an in-memory fake without standing
// up Postgres.
type storeAPI interface {
	Acquire(ctx context.Context, vehicleID string) (bool, error)
	Renew(ctx context.Context) ([]string, error)
	ReleaseAll(ctx context.Context) (int, error)
}

// NewCoordinator builds a coordinator. onAcquire/onRelease must be
// non-nil; the rest are optional (logger defaults to slog.Default,
// onCountChange and reconcile-tuning to sane defaults).
func NewCoordinator(
	store *Store,
	vehicles func(ctx context.Context) ([]string, error),
	onAcquire, onRelease func(vehicleID string),
	logger *slog.Logger,
) *Coordinator {
	return newCoordinator(store, vehicles, onAcquire, onRelease, logger, store.ttl/4)
}

func newCoordinator(
	store storeAPI,
	vehicles func(ctx context.Context) ([]string, error),
	onAcquire, onRelease func(vehicleID string),
	logger *slog.Logger,
	reconcileInterval time.Duration,
) *Coordinator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Coordinator{
		store:             store,
		log:               logger,
		vehicles:          vehicles,
		onAcquire:         onAcquire,
		onRelease:         onRelease,
		reconcileInterval: reconcileInterval,
		trigger:           make(chan struct{}, 1),
		owned:             make(map[string]struct{}),
	}
}

// TriggerReconcile asks the Run loop to reconcile on its next
// scheduling pass instead of waiting for the next tick. Safe to
// call concurrently and from any goroutine; multiple calls coalesce
// into a single reconcile (the channel is cap-1, send is
// non-blocking).
//
// Used at startup: RefreshVehicleInfo's success goroutine calls
// this so the first set of vehicles claims its leases within
// milliseconds of the REST reply, instead of waiting up to a full
// reconcileInterval.
func (c *Coordinator) TriggerReconcile() {
	if c == nil {
		return
	}
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

// SetCountObserver wires a callback that fires after every reconcile
// with the current number of leases owned. Safe to call before Run.
func (c *Coordinator) SetCountObserver(fn func(count int)) {
	c.onCountChange = fn
}

// Run blocks until ctx is cancelled, reconciling leases on every
// tick. Returns ctx.Err() so callers can distinguish a clean shutdown
// from an unexpected exit.
//
// Reconciles once immediately so a freshly-started pod doesn't wait
// reconcileInterval before claiming any vehicles.
func (c *Coordinator) Run(ctx context.Context) error {
	c.reconcile(ctx)

	t := time.NewTicker(c.reconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			c.reconcile(ctx)
		case <-c.trigger:
			c.reconcile(ctx)
		}
	}
}

// reconcile is one tick of the loop. It (1) renews everything we
// already own, (2) tries to acquire any vehicle from the cluster set
// we don't yet own, (3) fires acquire/release callbacks for the diff.
//
// Errors in any individual step are logged and swallowed — this is a
// best-effort coordination layer; a transient Postgres blip should
// not crash every pod. The next tick will reconcile.
func (c *Coordinator) reconcile(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, c.reconcileInterval/2)
	defer cancel()

	// Renew first. Any vehicle missing from the renew result is one
	// we lost (someone stole it because we let the TTL lapse, or it
	// was deleted). Fire the release callback for those.
	stillOwned, err := c.store.Renew(rctx)
	if err != nil {
		c.log.Warn("lease renew failed", "err", err.Error())
	}

	stillOwnedSet := make(map[string]struct{}, len(stillOwned))
	for _, vid := range stillOwned {
		stillOwnedSet[vid] = struct{}{}
	}

	// Diff: anything in c.owned that isn't in stillOwnedSet was lost.
	c.mu.Lock()
	lost := make([]string, 0)
	for vid := range c.owned {
		if _, ok := stillOwnedSet[vid]; !ok {
			lost = append(lost, vid)
		}
	}
	c.owned = stillOwnedSet
	c.mu.Unlock()
	for _, vid := range lost {
		c.log.Info("lease lost", "vehicle", vid)
		c.onRelease(vid)
	}

	// Pull the cluster's vehicle set and try to acquire anything we
	// don't already own.
	vehicles, err := c.vehicles(rctx)
	if err != nil {
		c.log.Warn("lease vehicles list failed", "err", err.Error())
		c.fireCount()
		return
	}

	for _, vid := range vehicles {
		c.mu.Lock()
		_, alreadyOwned := c.owned[vid]
		c.mu.Unlock()
		if alreadyOwned {
			continue
		}
		got, err := c.store.Acquire(rctx, vid)
		if err != nil {
			c.log.Warn("lease acquire failed", "vehicle", vid, "err", err.Error())
			continue
		}
		if got {
			c.mu.Lock()
			c.owned[vid] = struct{}{}
			c.mu.Unlock()
			c.log.Info("lease acquired", "vehicle", vid)
			c.onAcquire(vid)
		}
	}

	c.fireCount()
}

func (c *Coordinator) fireCount() {
	if c.onCountChange == nil {
		return
	}
	c.mu.Lock()
	n := len(c.owned)
	c.mu.Unlock()
	c.onCountChange(n)
}

// Shutdown drops every lease this pod owns and fires onRelease for
// each. Used from the SIGTERM hook so a planned pod restart hands
// vehicles to surviving pods immediately. Best-effort with a
// caller-supplied (timeout-bound) context — if Postgres is
// unreachable the leases will still age out via the TTL.
func (c *Coordinator) Shutdown(ctx context.Context) {
	c.mu.Lock()
	owned := make([]string, 0, len(c.owned))
	for vid := range c.owned {
		owned = append(owned, vid)
	}
	c.owned = make(map[string]struct{})
	c.mu.Unlock()

	if n, err := c.store.ReleaseAll(ctx); err != nil {
		c.log.Warn("lease release-all failed", "err", err.Error())
	} else {
		c.log.Info("lease release-all", "count", n)
	}

	for _, vid := range owned {
		c.onRelease(vid)
	}
	c.fireCount()
}
