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

	// groups are the fence tokens of the group leases held as of the last
	// RenewGroups, by group id. Guarded by groupMu.
	groupMu   sync.Mutex
	groups    map[string]int64
	groupOKAt time.Time // when the last successful RenewGroups was sent (monotonic)
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
	return &Store{db: db, podID: podID, ttl: DefaultTTL, groups: map[string]int64{}}, nil
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
	// One pod holds all of a user's vehicles: Rivian's gateway takes one
	// connection per session token, so a user split across pods would have them
	// kick each other off. The check and the insert run under a per-user advisory
	// lock; without it two pods acquiring two different vehicles of one user both
	// pass the check (write skew). A Rivian id can belong to several users (the
	// same VIN under two accounts), so every such user is locked, in sorted order
	// so two pods never take the same pair the other way round.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("acquire %s: %w", vehicleID, err)
	}
	defer func() { _ = tx.Rollback() }()

	users, err := vehicleUsers(ctx, tx, vehicleID)
	if err != nil {
		return false, fmt.Errorf("acquire %s: %w", vehicleID, err)
	}
	for _, u := range users {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('lease-user:' || $1::text, 0))`, u); err != nil {
			return false, fmt.Errorf("acquire %s: lock: %w", vehicleID, err)
		}
	}

	// The SELECT ... WHERE NOT EXISTS guards the fresh-insert path (the ON
	// CONFLICT predicate only filters the update path): refused when another pod
	// holds an unexpired lease on a different vehicle of the same user group (users
	// linked through a shared Rivian id; see userGroups).
	const q = `
        INSERT INTO subscription_leases (vehicle_id, pod_id, acquired_at, renewed_at, expires_at)
        SELECT $1, $2, now(), now(), now() + $3::interval
         WHERE NOT EXISTS (
               SELECT 1
                 FROM subscription_leases l
                 JOIN vehicles lv ON lv.rivian_vehicle_id = l.vehicle_id
                WHERE lv.user_id::text = ANY($4::text[])
                  AND l.vehicle_id <> $1
                  AND l.pod_id <> $2
                  AND l.expires_at > now())
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
	err = tx.QueryRowContext(ctx, q, vehicleID, s.podID, fmt.Sprintf("%d seconds", int(s.ttl.Seconds())), users).Scan(&winner)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing written: a healthy lease held by another pod, or a sibling of
		// this user's held elsewhere. Not our error, just not our turn.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire %s: %w", vehicleID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("acquire %s: commit: %w", vehicleID, err)
	}
	return winner == s.podID, nil
}

// userGroups is a CTE labelling every user with the smallest id of the group of
// users linked through a shared Rivian id (the same VIN under two accounts).
// Most users are a group of one. Acquire and SplitVehicles must use the same
// groups: if only one did, one would refuse what the other keeps and the pods
// would flap. (Without the linking, user A {X, Y} and user B {Y, Z} with pod 1 on
// X and pod 2 on Z leave Y refused on both pods and unrecorded.)
const userGroups = `
	WITH RECURSIVE edges AS (
	    SELECT DISTINCT v1.user_id AS a, v2.user_id AS b
	      FROM vehicles v1 JOIN vehicles v2 ON v2.rivian_vehicle_id = v1.rivian_vehicle_id
	), reach(a, b) AS (
	    SELECT a, b FROM edges
	    UNION
	    SELECT r.a, e.b FROM reach r JOIN edges e ON e.a = r.b
	), grp AS (
	    SELECT a AS user_id, min(b::text) AS gid FROM reach GROUP BY a
	)`

// vehicleUsers returns every user in the group of the users that have a vehicles
// row for this Rivian id, sorted (the lock order). None for a vehicle with no row.
func vehicleUsers(ctx context.Context, tx *sql.Tx, vehicleID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, userGroups+`
	SELECT DISTINCT g2.user_id::text
	  FROM vehicles v
	  JOIN grp g1 ON g1.user_id = v.user_id
	  JOIN grp g2 ON g2.gid = g1.gid
	 WHERE v.rivian_vehicle_id = $1
	 ORDER BY 1`, vehicleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SplitVehicles lists the vehicles this pod holds whose user also has an
// unexpired lease on another vehicle held by a pod with a LOWER pod id. Such a
// user is split across pods; the greater pod id gives its share up, so both
// pods never release at once and the split converges. The Coordinator releases
// them (unsubscribe first, then Release).
func (s *Store) SplitVehicles(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, userGroups+`
        SELECT DISTINCT l.vehicle_id
          FROM subscription_leases l
          JOIN vehicles v  ON v.rivian_vehicle_id = l.vehicle_id
          JOIN grp g1      ON g1.user_id = v.user_id
          JOIN grp g2      ON g2.gid = g1.gid
          JOIN vehicles v2 ON v2.user_id = g2.user_id
          JOIN subscription_leases l2 ON l2.vehicle_id = v2.rivian_vehicle_id
         WHERE l.pod_id = $1
           AND l2.vehicle_id <> l.vehicle_id
           AND l2.pod_id < $1
           AND l2.expires_at > now()`, s.podID)
	if err != nil {
		return nil, fmt.Errorf("split vehicles: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
	// The group rows go with the vehicle rows; a failure here only leaves them to expire.
	_ = s.releaseGroups(ctx)
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

	// onGroups and onDryRun report the group leases (SetGroupObserver).
	onGroups func(held int)
	onDryRun func(source string, rejected int)

	// authoritative, when set, returns the vehicle IDs this pod should
	// hold a lease for right now. A lease this pod owns whose vehicle is
	// absent from the set is reaped: reconcile releases it and fires
	// onRelease so the stale subscription is torn down instead of
	// renewed forever. A vehicle leaves the set when it stops being
	// subscribable - the owner deleted their account (vehicles row gone)
	// or disconnected (stored session gone). Acquisition is gated on the
	// same set so a self-referential vehicle source (the leases table
	// feeds its own candidate list) can't resurrect a vehicle that
	// should no longer be leased. The coordinator treats the set as
	// opaque; what makes a vehicle authoritative is the caller's
	// predicate. nil disables reaping - single-binary boot and tests
	// keep the pre-existing behaviour.
	authoritative func(ctx context.Context) ([]string, error)

	reconcileInterval time.Duration

	// trigger lets external callers (e.g. the StateMonitor's
	// post-RefreshVehicleInfo goroutine) ask the coordinator to
	// reconcile immediately rather than wait for the next tick.
	// Buffered cap-1 with non-blocking send: many simultaneous
	// pokes coalesce into a single reconcile.
	trigger chan struct{}

	mu    sync.Mutex
	owned map[string]struct{}
	// closing is set when Shutdown begins: a reconcile already in flight must not
	// acquire, resubscribe or heal anything from then on, or it could start a vehicle
	// Shutdown just stopped, right before the lease is released. Guarded by mu.
	closing bool

	// streaming, when set, reports whether this pod is actually
	// subscribed to a vehicle it holds the lease for. Holding a lease
	// without a subscription is silent data loss: Sep 7–27 2026, two
	// trucks recorded nothing for 20 days while their lease was renewed
	// every tick, because onAcquire only fires on acquisition. Reconcile
	// re-fires onAcquire for such a vehicle (EnsureSubscribed is
	// idempotent), at most every unstreamedRetry, and reports each
	// episode once to onUnstreamed (a metric) with a Warn. nil disables
	// the check.
	streaming    func(vehicleID string) bool
	onUnstreamed func(vehicleID string)
	// unstreamed[vehicleID] is when the current episode's last
	// re-subscribe was fired. Guarded by mu.
	unstreamed map[string]time.Time
	now        func() time.Time

	// lastRenew is when a renew last succeeded (set at construction, so a fresh
	// coordinator is not fenced at once). A failed renew keeps the leases until
	// they could have expired.
	lastRenew time.Time
}

// unstreamedRetry bounds how often reconcile re-fires onAcquire for a
// vehicle that stays unsubscribed (a monitor that can't start for it
// would otherwise be poked every tick).
const unstreamedRetry = 5 * time.Minute

// storeAPI is the slice of *Store the Coordinator actually uses.
// Pulled out so tests can supply an in-memory fake without standing
// up Postgres.
type storeAPI interface {
	Acquire(ctx context.Context, vehicleID string) (bool, error)
	Renew(ctx context.Context) ([]string, error)
	Release(ctx context.Context, vehicleID string) error
	ReleaseAll(ctx context.Context) (int, error)
}

// groupRenewer is the optional part of a store that writes group leases (M1 of the
// single-owner design). The real Store has it; the fakes in unit tests need not.
type groupRenewer interface {
	RenewGroups(ctx context.Context) (GroupResult, error)
}

// splitFinder is the optional part of a store that can find users split across
// pods. The real Store has it; the fakes in unit tests need not.
type splitFinder interface {
	SplitVehicles(ctx context.Context) ([]string, error)
}

// handoffWait bounds how long healing waits for a released vehicle's
// subscription to stop before it gives the lease up.
const handoffWait = 5 * time.Second

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
		lastRenew:         time.Now(),
		store:             store,
		log:               logger,
		vehicles:          vehicles,
		onAcquire:         onAcquire,
		onRelease:         onRelease,
		reconcileInterval: reconcileInterval,
		trigger:           make(chan struct{}, 1),
		owned:             make(map[string]struct{}),
		unstreamed:        make(map[string]time.Time),
		now:               time.Now,
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

// SetStreamingCheck wires the "is this pod subscribed to the vehicle"
// probe that lets reconcile heal a lease held without a subscription.
// Safe to call before Run.
func (c *Coordinator) SetStreamingCheck(fn func(vehicleID string) bool) {
	c.streaming = fn
}

// SetUnstreamedObserver wires a callback fired once per episode of a
// lease held without a subscription (the metric). Safe to call before
// Run.
func (c *Coordinator) SetUnstreamedObserver(fn func(vehicleID string)) {
	c.onUnstreamed = fn
}

// healUnstreamed re-fires onAcquire for every owned vehicle this pod
// isn't subscribed to (see streaming). Runs after renew/reap, before
// acquisition, so a vehicle acquired this tick isn't judged before its
// subscription had a chance to start.
func (c *Coordinator) healUnstreamed() {
	if c.streaming == nil || c.isClosing() {
		return
	}
	c.mu.Lock()
	owned := make([]string, 0, len(c.owned))
	for vid := range c.owned {
		owned = append(owned, vid)
	}
	c.mu.Unlock()
	now := c.now()
	for _, vid := range owned {
		if c.streaming(vid) {
			c.mu.Lock()
			_, was := c.unstreamed[vid]
			delete(c.unstreamed, vid)
			c.mu.Unlock()
			if was {
				c.log.Info("lease streaming again", "vehicle", vid)
			}
			continue
		}
		c.mu.Lock()
		last, inEpisode := c.unstreamed[vid]
		due := !inEpisode || now.Sub(last) >= unstreamedRetry
		if due {
			c.unstreamed[vid] = now
		}
		c.mu.Unlock()
		if !inEpisode {
			c.log.Warn("lease held, not streaming; resubscribing", "vehicle", vid)
			if c.onUnstreamed != nil {
				c.onUnstreamed(vid)
			}
		}
		if due && !c.isClosing() {
			c.onAcquire(vid)
		}
	}
	// Episodes end with the lease too.
	c.mu.Lock()
	for vid := range c.unstreamed {
		if _, ok := c.owned[vid]; !ok {
			delete(c.unstreamed, vid)
		}
	}
	c.mu.Unlock()
}

// SetAuthoritative wires the existence source used to reap orphan
// leases — vehicles whose backing row was deleted while a lease
// lingered (subscription_leases has no FK to users, so a deleted
// tenant's leases survive the cascade). Without it those leases are
// renewed forever and the owning pod's recorder keeps failing FK
// inserts. Safe to call before Run.
func (c *Coordinator) SetAuthoritative(fn func(ctx context.Context) ([]string, error)) {
	c.authoritative = fn
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

// leaseTTL is the lease lifetime the loop was built for: it renews every TTL/4.
func (c *Coordinator) leaseTTL() time.Duration { return c.reconcileInterval * 4 }

// pastFence reports whether the last good renew is so old that the leases may
// have expired (and been taken by a peer): a TTL since it, less one tick.
func (c *Coordinator) pastFence() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now().Sub(c.lastRenew) >= c.leaseTTL()-c.reconcileInterval
}

// fenceAll gives up every lease the coordinator thinks it holds: it can no longer
// show they are still its own.
func (c *Coordinator) fenceAll() {
	c.mu.Lock()
	lost := make([]string, 0, len(c.owned))
	for vid := range c.owned {
		lost = append(lost, vid)
	}
	c.owned = make(map[string]struct{})
	c.mu.Unlock()
	for _, vid := range lost {
		c.log.Warn("lease fenced: renewals have failed for a whole TTL", "vehicle", vid)
		c.onRelease(vid)
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
	if c.isClosing() {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, c.reconcileInterval/2)
	defer cancel()

	// Renew first. Any vehicle missing from the renew result is one
	// we lost (someone stole it because we let the TTL lapse, or it
	// was deleted). Fire the release callback for those.
	stillOwned, err := c.store.Renew(rctx)
	if err != nil {
		// A renew that failed says nothing about who owns what: the database blipped
		// or the call timed out. Dropping every subscription here would stop all the
		// recording for one bad second. Keep what we hold; only once the leases could
		// have run out (a TTL since the last renew that worked, less one tick) are
		// they no longer ours to act on, and then self-fence.
		c.log.Warn("lease renew failed", "err", err.Error())
		if c.pastFence() {
			c.fenceAll()
		}
		c.fireCount()
		return
	}
	c.mu.Lock()
	c.lastRenew = c.now()
	c.mu.Unlock()

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

	// Reap orphans: leases we still hold whose backing vehicle row is
	// gone. Renew (keyed on pod_id, not on whether the vehicle exists)
	// keeps renewing them, so without this a deleted vehicle's lease
	// lives forever and the owning pod's recorder spams FK-violation
	// inserts. Only runs when an authoritative existence source is
	// wired, and never on a query error — reaping on a transient DB
	// blip would tear down every live subscription. `live` also gates
	// acquisition below so a self-referential vehicle source can't
	// re-acquire what we just released.
	var live map[string]struct{}
	if c.authoritative != nil {
		ids, aerr := c.authoritative(rctx)
		if aerr != nil {
			c.log.Warn("lease authoritative list failed; skipping orphan reap", "err", aerr.Error())
		} else {
			live = make(map[string]struct{}, len(ids))
			for _, id := range ids {
				live[id] = struct{}{}
			}
			c.mu.Lock()
			orphans := make([]string, 0)
			for vid := range c.owned {
				if _, ok := live[vid]; !ok {
					orphans = append(orphans, vid)
				}
			}
			c.mu.Unlock()
			for _, vid := range orphans {
				if rerr := c.store.Release(rctx, vid); rerr != nil {
					c.log.Warn("orphan lease release failed", "vehicle", vid, "err", rerr.Error())
					continue
				}
				c.mu.Lock()
				delete(c.owned, vid)
				c.mu.Unlock()
				c.log.Info("orphan lease reaped (vehicle no longer exists)", "vehicle", vid)
				c.onRelease(vid)
			}
		}
	}

	c.healSplitUsers(rctx)

	c.healUnstreamed()

	// Pull the cluster's vehicle set and try to acquire anything we
	// don't already own.
	vehicles, err := c.vehicles(rctx)
	if err != nil {
		c.log.Warn("lease vehicles list failed", "err", err.Error())
		c.fireCount()
		return
	}

	for _, vid := range vehicles {
		if c.isClosing() {
			break
		}
		if live != nil {
			if _, ok := live[vid]; !ok {
				// Candidate surfaced by the monitor/leases source with no
				// backing vehicles row. It can't be subscribed anyway
				// (owner resolution needs the row), and leasing it would
				// only recreate an orphan — skip.
				continue
			}
		}
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
			closing := c.closing
			if !closing {
				c.owned[vid] = struct{}{}
			}
			c.mu.Unlock()
			if closing {
				// Shutdown began while this acquire ran: give it straight back.
				_ = c.store.Release(rctx, vid)
				break
			}
			c.log.Info("lease acquired", "vehicle", vid)
			c.onAcquire(vid)
		}
	}

	c.renewGroups(rctx)
	c.fireCount()
}

// SetGroupObserver wires the metrics for the group leases: held is told the number
// of groups this pod holds after every pass, dryRun the number of tokens that
// would have been refused. Optional.
func (c *Coordinator) SetGroupObserver(held func(n int), dryRun func(source string, n int)) {
	c.onGroups, c.onDryRun = held, dryRun
}

// renewGroups writes the group leases next to the vehicle leases. Nothing acts on
// them in M1: a failure is logged and the next tick tries again.
func (c *Coordinator) renewGroups(ctx context.Context) {
	gr, ok := c.store.(groupRenewer)
	if !ok || c.isClosing() {
		return
	}
	res, err := gr.RenewGroups(ctx)
	if err != nil {
		c.log.Warn("group lease renew failed", "err", err.Error())
		// Renewals failing past the local deadline: a guarded write would be refused here with no
		// database call (counted, nothing acts on it in M1).
		if lp, ok := c.store.(interface{ PastLocalDeadline() bool }); ok && c.onDryRun != nil && lp.PastLocalDeadline() {
			c.onDryRun("local", 1)
		}
		return
	}
	for _, g := range res.DryRunRejected {
		c.log.Warn("group lease token would be refused (dry run)", "group", g)
	}
	if c.onGroups != nil {
		c.onGroups(res.Held)
	}
	if c.onDryRun != nil && len(res.DryRunRejected) > 0 {
		c.onDryRun("db", len(res.DryRunRejected))
	}
}

// healSplitUsers gives up the vehicles of a user that is also held by a pod with
// a lower id (see Store.SplitVehicles), so a split left by a rollout, or by the
// old per-vehicle rule, converges onto one pod. The order matters: stop the
// subscription first (onRelease), wait a bounded moment for it to actually stop,
// and only then release the lease. Releasing first would let the other pod dial
// while this connection is still up, the very collision this removes.
func (c *Coordinator) healSplitUsers(ctx context.Context) {
	if c.isClosing() {
		return
	}
	sf, ok := c.store.(splitFinder)
	if !ok {
		return
	}
	split, err := sf.SplitVehicles(ctx)
	if err != nil {
		c.log.Warn("lease split check failed", "err", err.Error())
		return
	}
	for _, vid := range split {
		c.mu.Lock()
		_, mine := c.owned[vid]
		c.mu.Unlock()
		if !mine {
			continue
		}
		c.log.Info("lease released: the user's other vehicles are held by a lower pod id", "vehicle", vid)
		c.onRelease(vid)
		c.waitStopped(ctx, vid)
		if rerr := c.store.Release(ctx, vid); rerr != nil {
			c.log.Warn("split lease release failed", "vehicle", vid, "err", rerr.Error())
			continue
		}
		c.mu.Lock()
		delete(c.owned, vid)
		c.mu.Unlock()
	}
}

// waitStopped waits, up to handoffWait, until the streaming check says this pod
// no longer subscribes to vid. Without a check there is nothing to wait on.
func (c *Coordinator) waitStopped(ctx context.Context, vid string) {
	if c.streaming == nil {
		return
	}
	deadline := time.Now().Add(handoffWait)
	for c.streaming(vid) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
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
// each, stopping the subscriptions first. Used from the SIGTERM hook so a planned pod restart hands
// vehicles to surviving pods immediately. Best-effort with a
// caller-supplied (timeout-bound) context — if Postgres is
// unreachable the leases will still age out via the TTL.
func (c *Coordinator) Shutdown(ctx context.Context) {
	c.mu.Lock()
	c.closing = true
	owned := make([]string, 0, len(c.owned))
	for vid := range c.owned {
		owned = append(owned, vid)
	}
	c.mu.Unlock()

	// Stop the subscriptions first, wait (bounded) until they have stopped, and only
	// then give the leases up. Releasing first lets a peer dial a vehicle while this
	// pod's WebSocket for it is still up: the collision the lease healing avoids.
	for _, vid := range owned {
		c.onRelease(vid)
	}
	c.waitAllStopped(ctx, owned)

	c.mu.Lock()
	c.owned = make(map[string]struct{})
	c.mu.Unlock()
	if n, err := c.store.ReleaseAll(ctx); err != nil {
		c.log.Warn("lease release-all failed", "err", err.Error())
	} else {
		c.log.Info("lease release-all", "count", n)
	}
	c.fireCount()
}

func (c *Coordinator) isClosing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closing
}

// waitAllStopped waits until none of vids is streaming, for at most handoffWait
// and at most two thirds of what is left of ctx, so ReleaseAll always keeps time
// to run. Without a streaming check there is nothing to wait on.
func (c *Coordinator) waitAllStopped(ctx context.Context, vids []string) {
	if c.streaming == nil || len(vids) == 0 {
		return
	}
	limit := handoffWait
	if dl, ok := ctx.Deadline(); ok {
		if share := time.Until(dl) * 2 / 3; share < limit {
			limit = share
		}
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		busy := false
		for _, vid := range vids {
			if c.streaming(vid) {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
	c.log.Warn("lease shutdown: subscriptions still running; releasing anyway", "waited", limit.String())
}
