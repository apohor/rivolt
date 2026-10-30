package rivian

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
)

// MonitorRegistry holds one *StateMonitor per user. Each monitor
// is wired to that user's *LiveClient and that user's per-user
// data-plane stores (drives/charges/samples/settings), so the
// recorder writes land under the correct user_id without any
// global "current user" assumption.
//
// Lifecycle:
//   - Start(ctx, uid) is idempotent. It builds and starts a monitor
//     on first call for that uid, no-ops on later calls.
//   - Stop(uid) cancels that user's monitor ctx and removes the
//     entry. Used on logout / explicit reauth-required transitions.
//   - For(uid) returns the monitor (or nil if not started). Request
//     handlers use this for /api/state, /api/live-session, etc.
//   - EnsureSubscribed/Unsubscribe route by rivian_vehicle_id →
//     vehicle owner uid → that user's monitor. The lease
//     coordinator stays user-agnostic; this registry resolves
//     ownership at dispatch time.
//   - AllVehicleInfo unions every monitor's known vehicles so the
//     coordinator's vehicleSource sees one flat set.
//
// The registry records for live accounts and for replay accounts
// (RIVIAN_CLIENT=replay, source-fed monitors). Mock and stub paths
// don't have a recorder.
type MonitorRegistry struct {
	pool      *sql.DB
	accounts  AccountRegistry
	drives    *drives.Factory
	charges   *charges.Factory
	samples   *samples.Factory
	settings  *settings.Factory
	elevation ElevationLookup
	fenceSrc  fence.Source
	// wanted are subscriptions EnsureSubscribed could not start because the owner has no monitor yet
	// (vehicle -> owner); Start runs them when the owner's monitor comes up, so a lease acquired before
	// the monitor does not wait for the next lease tick to stream. Guarded by mu.
	wanted             map[string]uuid.UUID
	onFenced           func(kind string)
	routeFiller        RouteFiller
	liveStateStore     func(uid uuid.UUID) LiveStateStore
	driveCloseHook     func(uid uuid.UUID) DriveCloseHook
	extraDriveClose    []func(uid uuid.UUID) DriveCloseHook
	chargeCloseHook    func(uid uuid.UUID) ChargeCloseHook
	extraChargeClose   []func(uid uuid.UUID) ChargeCloseHook
	batteryCapacityFor func(uid uuid.UUID) func(vehicleID string, kwh float64)
	vehicleInfoFor     func(uid uuid.UUID) func([]Vehicle)
	dcfcPriceFor       func(uid uuid.UUID) DCFCPriceLookup
	dcfcDetectFor      func(uid uuid.UUID) DCFCDetector
	dcfcEstimateFor    func(uid uuid.UUID) DCFCEstimator
	rehydrate          func(ctx context.Context, uid uuid.UUID) bool
	sleepSafe          func(vehicleID string) bool
	sleepObserver      SleepObserver
	wakeSafety         func(vehicleID string) WakeSafety
	logger             *slog.Logger
	// stagger is shared by every monitor the registry creates: the whole
	// process spaces its first WebSocket connects, not each user on their own.
	stagger *Stagger

	mu       sync.RWMutex
	monitors map[uuid.UUID]*monitorEntry
	parent   context.Context //nolint:containedctx // outer ctx for spawned monitors
}

type monitorEntry struct {
	monitor *StateMonitor
	cancel  context.CancelFunc
}

// NewMonitorRegistry constructs a registry. Start(ctx, uid) is what
// actually launches per-user monitors; the parent ctx passed there
// (typically the server ctx) bounds every monitor's lifetime.
func NewMonitorRegistry(
	pool *sql.DB,
	accounts AccountRegistry,
	drivesF *drives.Factory,
	chargesF *charges.Factory,
	samplesF *samples.Factory,
	settingsF *settings.Factory,
	logger *slog.Logger,
) *MonitorRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &MonitorRegistry{
		pool:     pool,
		accounts: accounts,
		drives:   drivesF,
		charges:  chargesF,
		samples:  samplesF,
		settings: settingsF,
		logger:   logger,
		stagger:  NewStagger(defaultStaggerInterval),
		monitors: make(map[uuid.UUID]*monitorEntry),
	}
}

// SetParent records the outer ctx all per-user monitors derive
// their lifetime from. Must be called once at boot before Start.
// SetFence wires the group-lease fence into every monitor the registry builds (see
// StateMonitor.SetFence). Call before monitors start.
func (r *MonitorRegistry) SetFence(src fence.Source, onFenced func(kind string)) {
	r.mu.Lock()
	r.fenceSrc, r.onFenced = src, onFenced
	r.mu.Unlock()
}

func (r *MonitorRegistry) SetParent(ctx context.Context) {
	r.mu.Lock()
	r.parent = ctx
	r.mu.Unlock()
}

// SetElevationLookup wires an optional elevation resolver shared
// across every per-user monitor the registry spawns. Safe to call at
// boot before any Start, idempotent thereafter (the resolver itself
// is stateless w.r.t. user identity -- altitude is a pure function
// of lat/lon, not a per-user query).
func (r *MonitorRegistry) SetElevationLookup(e ElevationLookup) {
	r.mu.Lock()
	r.elevation = e
	r.mu.Unlock()
}

// SetRouteFiller wires the optional GPS-gap fill backend shared
// across every per-user monitor. Stateless w.r.t. user identity, so
// one client serves all users.
func (r *MonitorRegistry) SetRouteFiller(rf RouteFiller) {
	r.mu.Lock()
	r.routeFiller = rf
	r.mu.Unlock()
}

// SetLiveStateStoreFactory wires a per-user LiveStateStore factory.
// On each new monitor spawn, factory(uid) is called and the result
// (may be nil) is handed to the StateMonitor so the recorder can
// rehydrate / persist its in-flight session accumulators across pod
// restarts and lease handoffs. Safe to call at boot before any Start.
func (r *MonitorRegistry) SetLiveStateStoreFactory(factory func(uid uuid.UUID) LiveStateStore) {
	r.mu.Lock()
	r.liveStateStore = factory
	r.mu.Unlock()
}

// SetBatteryCapacityHookFactory wires a per-user write-through hook
// for the live-reported usable pack capacity. Each new monitor gets
// factory(uid) and installs it via SetBatteryCapacityHook; the hook
// runs whenever Rivian's vehicleState.batteryCapacity changes for
// one of the user's vehicles. Typically wires through to a SQL
// UPDATE on vehicles.pack_kwh so a future restart sees the right
// nameplate without having to wait for another live observation.
func (r *MonitorRegistry) SetBatteryCapacityHookFactory(factory func(uid uuid.UUID) func(vehicleID string, kwh float64)) {
	r.mu.Lock()
	r.batteryCapacityFor = factory
	r.mu.Unlock()
}

// SetVehicleInfoHookFactory wires the per-user persister for the vehicle
// metadata (trim, image, pack guess) each monitor fetches in
// RefreshVehicleInfo.
func (r *MonitorRegistry) SetVehicleInfoHookFactory(factory func(uid uuid.UUID) func([]Vehicle)) {
	r.mu.Lock()
	r.vehicleInfoFor = factory
	r.mu.Unlock()
}

// SetDriveCloseHookFactory wires a per-user DriveCloseHook factory.
// On each new monitor spawn, factory(uid) is called and the result
// (may be nil) is handed to the StateMonitor so the recorder can
// run async post-close enrichment (e.g. weather fetch) as drives
// are recorded — instead of deferring to a manual SPA backfill.
// Safe to call at boot before any Start.
func (r *MonitorRegistry) SetDriveCloseHookFactory(factory func(uid uuid.UUID) DriveCloseHook) {
	r.mu.Lock()
	r.driveCloseHook = factory
	r.mu.Unlock()
}

// SetDCFCDetectorFactory wires per-user fast-charge network detection
// (on only where the dcfc_auto_network flag covers the user; the
// detector itself says when it's off). Safe to call at boot before any
// Start.
func (r *MonitorRegistry) SetDCFCDetectorFactory(factory func(uid uuid.UUID) DCFCDetector) {
	r.mu.Lock()
	r.dcfcDetectFor = factory
	r.mu.Unlock()
}

// SetDCFCEstimatorFactory wires the per-user typical-price fallback for
// fast charges the user has no rate for. Live recorder only. Safe to call
// at boot before any Start.
func (r *MonitorRegistry) SetDCFCEstimatorFactory(factory func(uid uuid.UUID) DCFCEstimator) {
	r.mu.Lock()
	r.dcfcEstimateFor = factory
	r.mu.Unlock()
}

// SetDCFCPriceLookupFactory wires a per-user fast-charge pricer (the
// user's network memberships pick the rate). Safe to call at boot
// before any Start.
func (r *MonitorRegistry) SetDCFCPriceLookupFactory(factory func(uid uuid.UUID) DCFCPriceLookup) {
	r.mu.Lock()
	r.dcfcPriceFor = factory
	r.mu.Unlock()
}

// SetSessionRehydrator wires a callback that loads a user's persisted
// Rivian session from shared storage and restores it into their cached
// per-pod client, returning true when the client ends up authenticated.
// The lease coordinator can hand this pod a vehicle whose owner signed
// in on a peer pod, so the cached client here predates the session and
// reports "not authenticated" — the monitor would then wait on
// AuthReady forever and record nothing. EnsureSubscribed calls this to
// self-heal without waiting for a pod restart's boot hydrate. Returns
// false (and the subscribe stays a no-op) when no session is stored.
func (r *MonitorRegistry) SetSessionRehydrator(fn func(ctx context.Context, uid uuid.UUID) bool) {
	r.mu.Lock()
	r.rehydrate = fn
	r.mu.Unlock()
}

// SetChargeCloseHookFactory mirrors SetDriveCloseHookFactory for
// charge sessions. Used to fire per-user notifications and similar
// side-effects on charge close.
func (r *MonitorRegistry) SetChargeCloseHookFactory(factory func(uid uuid.UUID) ChargeCloseHook) {
	r.mu.Lock()
	r.chargeCloseHook = factory
	r.mu.Unlock()
}

// AddDriveCloseHookFactory registers a further drive-close side effect next to
// the one SetDriveCloseHookFactory holds (weather), the way
// AddChargeCloseHookFactory does for charges. Every registered hook runs, in
// order registered, for each close.
func (r *MonitorRegistry) AddDriveCloseHookFactory(factory func(uid uuid.UUID) DriveCloseHook) {
	r.mu.Lock()
	r.extraDriveClose = append(r.extraDriveClose, factory)
	r.mu.Unlock()
}

// AddChargeCloseHookFactory registers a further charge-close side effect
// next to the one SetChargeCloseHookFactory holds (push notifications), so
// independent features can each react to a closed charge. Every registered
// hook runs, in order registered, for each close.
func (r *MonitorRegistry) AddChargeCloseHookFactory(factory func(uid uuid.UUID) ChargeCloseHook) {
	r.mu.Lock()
	r.extraChargeClose = append(r.extraChargeClose, factory)
	r.mu.Unlock()
}

// Start launches a monitor for uid if one is not already running.
// Returns the monitor (existing or new). Returns nil when the account
// is neither a LiveClient nor an authenticated FrameSource (mock/stub
// paths shouldn't be calling Start; this is a defensive guard).
func (r *MonitorRegistry) Start(ctx context.Context, uid uuid.UUID) *StateMonitor {
	if uid == uuid.Nil {
		return nil
	}
	r.mu.RLock()
	if e, ok := r.monitors[uid]; ok {
		r.mu.RUnlock()
		return e.monitor
	}
	parent := r.parent
	r.mu.RUnlock()
	if parent == nil {
		// SetParent wasn't called — caller should have wired the
		// server ctx at boot. Fall back to the passed-in ctx so we
		// don't silently break, but log loudly.
		r.logger.Warn("monitor registry: SetParent not called; using request ctx as parent")
		parent = ctx
	}

	// A live account gets a WS-subscribing monitor; an authenticated
	// FrameSource account (RIVIAN_CLIENT=replay) gets a source-fed one
	// with no Rivian client at all.
	acct := r.accounts.For(uid)
	lc := liveClientFromAccount(acct)
	src, isSource := acct.(FrameSource)
	if lc == nil && (!isSource || !acct.Authenticated()) {
		// User has no live session yet (or registry is mock/stub).
		// Login flow will call Start again once Login completes.
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.monitors[uid]; ok {
		return e.monitor
	}

	mctx, cancel := context.WithCancel(parent)
	var mon *StateMonitor
	if lc != nil {
		mon = NewStateMonitor(lc, r.logger.With("user_id", uid.String()))
	} else {
		var vehicles []Vehicle
		if c, ok := acct.(Client); ok {
			vehicles, _ = c.Vehicles(mctx)
		}
		mon = NewSourceMonitor(src, vehicles, r.logger.With("user_id", uid.String()))
	}
	mon.SetStagger(r.stagger)
	mon.SetFence(r.fenceSrc, r.onFenced)
	mon.SetStores(
		r.samples.For(uid),
		r.drives.For(uid),
		r.charges.For(uid),
	)
	if r.elevation != nil {
		mon.SetElevationLookup(r.elevation)
	}
	if r.routeFiller != nil {
		mon.SetRouteFiller(r.routeFiller)
	}
	if r.liveStateStore != nil {
		if s := r.liveStateStore(uid); s != nil {
			mon.SetLiveStateStore(s)
		}
	}
	{
		var hooks []DriveCloseHook
		if r.driveCloseHook != nil {
			if h := r.driveCloseHook(uid); h != nil {
				hooks = append(hooks, h)
			}
		}
		for _, f := range r.extraDriveClose {
			if h := f(uid); h != nil {
				hooks = append(hooks, h)
			}
		}
		if h := composeDriveClose(hooks); h != nil {
			mon.SetDriveCloseHook(h)
		}
	}
	{
		var hooks []ChargeCloseHook
		if r.chargeCloseHook != nil {
			if h := r.chargeCloseHook(uid); h != nil {
				hooks = append(hooks, h)
			}
		}
		for _, f := range r.extraChargeClose {
			if h := f(uid); h != nil {
				hooks = append(hooks, h)
			}
		}
		if h := composeChargeClose(hooks); h != nil {
			mon.SetChargeCloseHook(h)
		}
	}
	if r.vehicleInfoFor != nil {
		if h := r.vehicleInfoFor(uid); h != nil {
			mon.SetVehicleInfoHook(h)
		}
	}
	if r.batteryCapacityFor != nil {
		if h := r.batteryCapacityFor(uid); h != nil {
			mon.SetBatteryCapacityHook(h)
		}
	}
	if r.settings != nil {
		ss := r.settings.For(uid)
		if ss != nil {
			mon.SetPriceLookup(func() (float64, string) {
				cfg, err := settings.GetChargingConfig(mctx, ss)
				if err != nil {
					return 0, ""
				}
				return cfg.HomePricePerKWh, cfg.HomeCurrency
			})
			mon.SetHomeLookup(func() (float64, float64, bool) {
				h, err := settings.GetHomeLocation(mctx, ss)
				if err != nil || !h.Set {
					return 0, 0, false
				}
				return h.Latitude, h.Longitude, true
			})
		}
	}
	if r.dcfcPriceFor != nil {
		if fn := r.dcfcPriceFor(uid); fn != nil {
			mon.SetDCFCPriceLookup(fn)
		}
	}
	if r.dcfcDetectFor != nil {
		if fn := r.dcfcDetectFor(uid); fn != nil {
			mon.SetDCFCDetector(fn)
		}
	}
	if r.dcfcEstimateFor != nil {
		if fn := r.dcfcEstimateFor(uid); fn != nil {
			mon.SetDCFCEstimator(fn)
		}
	}
	mon.SetSleepSafe(r.sleepSafe)
	mon.SetSleepObserver(r.sleepObserver)
	mon.SetWakeSafety(r.wakeSafety)
	mon.Start(mctx)
	r.monitors[uid] = &monitorEntry{monitor: mon, cancel: cancel}
	r.logger.Info("monitor registry: started", "user_id", uid.String())
	r.runWanted(uid, mon)
	return mon
}

// SetSleepSafe installs the per-vehicle sleep-safe check handed to
// every monitor started afterwards (see StateMonitor.sleepGate).
func (r *MonitorRegistry) SetSleepSafe(fn func(vehicleID string) bool) {
	r.mu.Lock()
	r.sleepSafe = fn
	r.mu.Unlock()
}

// SetWakeSafety installs the wake-safety switch lookup handed to every
// monitor started afterwards (see wake_safety.go).
func (r *MonitorRegistry) SetWakeSafety(fn func(vehicleID string) WakeSafety) {
	r.mu.Lock()
	r.wakeSafety = fn
	r.mu.Unlock()
}

// SetSleepObserver installs the sleep metrics hook handed to every
// monitor started afterwards.
func (r *MonitorRegistry) SetSleepObserver(o SleepObserver) {
	r.mu.Lock()
	r.sleepObserver = o
	r.mu.Unlock()
}

// Stop tears down the monitor for uid. Idempotent.
func (r *MonitorRegistry) Stop(uid uuid.UUID) {
	r.mu.Lock()
	e, ok := r.monitors[uid]
	if ok {
		delete(r.monitors, uid)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	e.cancel()
	r.logger.Info("monitor registry: stopped", "user_id", uid.String())
}

// For returns the monitor for uid, or nil if not started. Safe for
// concurrent reads.
func (r *MonitorRegistry) For(uid uuid.UUID) *StateMonitor {
	if uid == uuid.Nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.monitors[uid]; ok {
		return e.monitor
	}
	return nil
}

// AllVehicleInfo unions every running monitor's vehicle set. Used
// by the lease coordinator's vehicleSource so it sees one flat
// list across all users.
func (r *MonitorRegistry) AllVehicleInfo() []Vehicle {
	r.mu.RLock()
	mons := make([]*StateMonitor, 0, len(r.monitors))
	for _, e := range r.monitors {
		mons = append(mons, e.monitor)
	}
	r.mu.RUnlock()
	var out []Vehicle
	for _, m := range mons {
		out = append(out, m.AllVehicleInfo()...)
	}
	return out
}

// EnsureSubscribed routes the rivian_vehicle_id to the owning
// user's monitor. Owner is resolved via vehicles.user_id; if the
// owner isn't tracked (no monitor running for that uid) the call
// is a no-op with a debug log — the lease will get re-evaluated
// once the owner signs in.
func (r *MonitorRegistry) EnsureSubscribed(rivianVehicleID string) {
	if rivianVehicleID == "" {
		return
	}
	uid, err := r.ownerOf(rivianVehicleID)
	if err != nil {
		r.logger.Warn("monitor registry: owner lookup failed",
			"rivian_vehicle_id", rivianVehicleID, "err", err.Error())
		return
	}
	mon := r.For(uid)
	if mon == nil {
		// We were handed this vehicle's lease but have no monitor for
		// its owner — they signed in on a peer pod, so our cached
		// client predates their session. Rehydrate it from shared
		// storage and start monitoring locally. If nothing is stored
		// the owner truly isn't signed in anywhere; stay a no-op and
		// let the lease re-evaluate once they do.
		r.mu.RLock()
		rehydrate, parent := r.rehydrate, r.parent
		r.mu.RUnlock()
		if rehydrate == nil || parent == nil || !rehydrate(parent, uid) {
			r.want(rivianVehicleID, uid)
			r.logger.Debug("monitor registry: no monitor for owner; subscribe when it starts",
				"rivian_vehicle_id", rivianVehicleID, "user_id", uid.String())
			return
		}
		mon = r.Start(parent, uid)
		if mon == nil {
			r.want(rivianVehicleID, uid)
			r.logger.Debug("monitor registry: lazy start yielded no monitor; subscribe when it starts",
				"rivian_vehicle_id", rivianVehicleID, "user_id", uid.String())
			return
		}
		r.logger.Info("monitor registry: rehydrated owner session and started monitor",
			"rivian_vehicle_id", rivianVehicleID, "user_id", uid.String())
	}
	mon.EnsureSubscribed(rivianVehicleID)
}

// want remembers a subscription that could not start yet (no monitor for its owner).
func (r *MonitorRegistry) want(vehicleID string, uid uuid.UUID) {
	r.mu.Lock()
	if r.wanted == nil {
		r.wanted = map[string]uuid.UUID{}
	}
	r.wanted[vehicleID] = uid
	r.mu.Unlock()
}

// runWanted starts the remembered subscriptions of uid, whose monitor has just come up. Called with
// r.mu held (from Start).
func (r *MonitorRegistry) runWanted(uid uuid.UUID, mon *StateMonitor) {
	for vid, u := range r.wanted {
		if u == uid {
			delete(r.wanted, vid)
			mon.EnsureSubscribed(vid)
			r.logger.Info("monitor registry: started a subscription that was waiting for the owner's monitor", "rivian_vehicle_id", vid)
		}
	}
}

// LatestAt is when the newest frame for the vehicle arrived on any of this pod's monitors, or the
// zero time when none has. The lease coordinator measures acquire-to-first-frame with it.
func (r *MonitorRegistry) LatestAt(vehicleID string) time.Time {
	r.mu.RLock()
	mons := make([]*StateMonitor, 0, len(r.monitors))
	for _, e := range r.monitors {
		mons = append(mons, e.monitor)
	}
	r.mu.RUnlock()
	var newest time.Time
	for _, m := range mons {
		if m == nil {
			continue
		}
		if _, at := m.Latest(vehicleID); at.After(newest) {
			newest = at
		}
	}
	return newest
}

// Streaming reports whether this pod runs a subscription for the
// vehicle on any of its monitors (including one parked waiting for
// auth or re-auth). The lease coordinator re-subscribes a leased
// vehicle that isn't streaming (SetStreamingCheck). Any monitor, not
// just the current owner's: if the owner resolution changed (users
// sharing a VIN), the old owner's run still counts, so the heal can't
// start a second subscription beside it. No database lookup: it runs
// inside every reconcile.
func (r *MonitorRegistry) Streaming(rivianVehicleID string) bool {
	r.mu.RLock()
	mons := make([]*StateMonitor, 0, len(r.monitors))
	for _, e := range r.monitors {
		mons = append(mons, e.monitor)
	}
	r.mu.RUnlock()
	for _, m := range mons {
		if m != nil && m.Subscribed(rivianVehicleID) {
			return true
		}
	}
	return false
}

// Unsubscribe tears the vehicle's subscription down on every monitor
// that runs it, not only its current owner's. After an owner flip (users
// sharing a VIN) the old owner's run is still the one streaming; routing
// by owner cancelled the new owner's (a no-op) and left the old one
// streaming on a pod that no longer holds the lease: a cross-pod
// duplicate. Unsubscribe on a monitor that doesn't run it is a no-op.
// No database lookup.
func (r *MonitorRegistry) Unsubscribe(rivianVehicleID string) {
	if rivianVehicleID == "" {
		return
	}
	r.mu.Lock()
	delete(r.wanted, rivianVehicleID) // no longer ours: do not start it later
	mons := make([]*StateMonitor, 0, len(r.monitors))
	for _, e := range r.monitors {
		mons = append(mons, e.monitor)
	}
	r.mu.Unlock()
	for _, m := range mons {
		if m != nil {
			m.Unsubscribe(rivianVehicleID)
		}
	}
}

// ownerOf resolves rivian_vehicle_id → the user_id whose session
// should drive the subscription. Errors are returned to the caller;
// sql.ErrNoRows means the vehicle is not in the table at all.
//
// A physical Rivian vehicle can end up owned by more than one Rivolt
// account — the classic case is a user who created a second account
// and connected the same car to both (see the onboarding UX work). The
// naive `LIMIT 1` then picked a row by physical order, which could bind
// the subscription to the WRONG account: bruce re-authed on one account
// while the monitor stayed parked under his other, still-needs_reauth
// account, so no telemetry flowed despite a valid session existing.
//
// Resolve deterministically to the owner most able to stream, in order:
// enabled over disabled, not-needing-reauth over needing it, has a
// stored rivian session over none, freshest session, then a stable
// created_at tiebreak. Single-owner vehicles are unaffected.
// ownerLookupTimeout bounds ownerOf's query.
const ownerLookupTimeout = 2 * time.Second

func (r *MonitorRegistry) ownerOf(rivianVehicleID string) (uuid.UUID, error) {
	if r.pool == nil {
		return uuid.Nil, errors.New("no db pool")
	}
	const q = `
		SELECT v.user_id
		FROM vehicles v
		JOIN users u ON u.id = v.user_id
		LEFT JOIN user_secrets s
		  ON s.user_id = v.user_id AND s.name = 'rivian.session'
		WHERE v.rivian_vehicle_id = $1
		ORDER BY
			u.disabled ASC,                    -- enabled accounts first
			(u.needs_reauth = FALSE) DESC,     -- healthy sessions first
			(s.user_id IS NOT NULL) DESC,      -- must have a stored session to stream
			s.updated_at DESC NULLS LAST,      -- freshest re-auth wins
			v.created_at ASC                   -- stable, deterministic tiebreak
		LIMIT 1`
	// Bounded: it runs inside the lease reconcile (acquire, release and
	// the not-streaming heal), and a hung connection there would stall
	// lease renewals until a peer took the vehicles over.
	ctx, cancel := context.WithTimeout(context.Background(), ownerLookupTimeout)
	defer cancel()
	var uid uuid.UUID
	err := r.pool.QueryRowContext(ctx, q, rivianVehicleID).Scan(&uid)
	if err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}

// composeChargeClose runs every hook, in order, for one closed charge. No
// hooks give nil; one is returned as is.
func composeChargeClose(hooks []ChargeCloseHook) ChargeCloseHook {
	switch len(hooks) {
	case 0:
		return nil
	case 1:
		return hooks[0]
	}
	return func(ctx context.Context, c charges.Charge) {
		for _, h := range hooks {
			h(ctx, c)
		}
	}
}

// composeDriveClose runs several drive-close hooks as one, in order.
func composeDriveClose(hooks []DriveCloseHook) DriveCloseHook {
	switch len(hooks) {
	case 0:
		return nil
	case 1:
		return hooks[0]
	}
	return func(ctx context.Context, d drives.Drive) {
		for _, h := range hooks {
			h(ctx, d)
		}
	}
}
