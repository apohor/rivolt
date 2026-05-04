package rivian

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
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
// The registry is wired only in live mode. Mock and stub paths
// don't have a recorder, so they don't need this layer.
type MonitorRegistry struct {
	pool     *sql.DB
	accounts AccountRegistry
	drives   *drives.Factory
	charges  *charges.Factory
	samples  *samples.Factory
	settings *settings.Factory
	logger   *slog.Logger

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
		monitors: make(map[uuid.UUID]*monitorEntry),
	}
}

// SetParent records the outer ctx all per-user monitors derive
// their lifetime from. Must be called once at boot before Start.
func (r *MonitorRegistry) SetParent(ctx context.Context) {
	r.mu.Lock()
	r.parent = ctx
	r.mu.Unlock()
}

// Start launches a monitor for uid if one is not already running.
// Returns the monitor (existing or new). Returns nil only when the
// account registry hands back a non-LiveClient (mock/stub paths
// shouldn't be calling Start; this is a defensive guard).
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

	lc := liveClientFromAccount(r.accounts.For(uid))
	if lc == nil {
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
	mon := NewStateMonitor(lc, r.logger.With("user_id", uid.String()))
	mon.SetStores(
		r.samples.For(uid),
		r.drives.For(uid),
		r.charges.For(uid),
	)
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
		}
	}
	mon.Start(mctx)
	r.monitors[uid] = &monitorEntry{monitor: mon, cancel: cancel}
	r.logger.Info("monitor registry: started", "user_id", uid.String())
	return mon
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
		r.logger.Debug("monitor registry: no monitor for owner; skip subscribe",
			"rivian_vehicle_id", rivianVehicleID, "user_id", uid.String())
		return
	}
	mon.EnsureSubscribed(rivianVehicleID)
}

// Unsubscribe routes the same way as EnsureSubscribed.
func (r *MonitorRegistry) Unsubscribe(rivianVehicleID string) {
	if rivianVehicleID == "" {
		return
	}
	uid, err := r.ownerOf(rivianVehicleID)
	if err != nil {
		// Best-effort: try every monitor. Ownership row may have
		// been deleted; we still want the WS torn down.
		r.mu.RLock()
		mons := make([]*StateMonitor, 0, len(r.monitors))
		for _, e := range r.monitors {
			mons = append(mons, e.monitor)
		}
		r.mu.RUnlock()
		for _, m := range mons {
			m.Unsubscribe(rivianVehicleID)
		}
		return
	}
	if mon := r.For(uid); mon != nil {
		mon.Unsubscribe(rivianVehicleID)
	}
}

// ownerOf resolves rivian_vehicle_id → user_id via the vehicles
// table. Errors are returned to the caller; ErrNoOwner means the
// vehicle is not in the table at all.
func (r *MonitorRegistry) ownerOf(rivianVehicleID string) (uuid.UUID, error) {
	if r.pool == nil {
		return uuid.Nil, errors.New("no db pool")
	}
	const q = `SELECT user_id FROM vehicles WHERE rivian_vehicle_id = $1 LIMIT 1`
	var uid uuid.UUID
	err := r.pool.QueryRowContext(context.Background(), q, rivianVehicleID).Scan(&uid)
	if err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}
