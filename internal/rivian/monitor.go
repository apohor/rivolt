package rivian

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// StateMonitor maintains a websocket subscription per vehicle and
// caches the latest pushed State. Callers read via Latest(); the
// subscription keeps it fresh in the background. Use Start() once per
// process to kick off the monitoring goroutines.
//
// Intended usage: one StateMonitor wrapping the live Client. The HTTP
// handler calls monitor.Latest(vehicleID) instead of client.State(),
// getting a cache-hit response without upstream cost. Missing-entry
// reads trigger a REST fallback to prime the cache while the
// subscription catches up.
type StateMonitor struct {
	client *LiveClient
	logger *slog.Logger

	mu       sync.RWMutex
	cache    map[string]*State
	stamp    map[string]time.Time
	active   map[string]context.CancelFunc
	parent   context.Context //nolint:containedctx // outer ctx for spawned subscriptions
	stopOnce sync.Once
}

// NewStateMonitor wraps a live client. Pass a logger (usually from
// main.go's structured logger). nil is allowed; events will be
// discarded.
func NewStateMonitor(client *LiveClient, logger *slog.Logger) *StateMonitor {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return &StateMonitor{
		client: client,
		logger: logger,
		cache:  make(map[string]*State),
		stamp:  make(map[string]time.Time),
		active: make(map[string]context.CancelFunc),
	}
}

// Start binds the monitor to a parent context. All subscriptions
// started via EnsureSubscribed use a child context derived from this
// parent; cancelling parent tears them all down.
func (m *StateMonitor) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent = ctx
}

// EnsureSubscribed guarantees a background subscription exists for
// the given vehicle. Safe to call concurrently; the first caller
// wins, subsequent callers are no-ops. If the subscription dies
// (e.g. ctx cancelled after an unauthenticated error) it stays
// removed so a future call can retry.
func (m *StateMonitor) EnsureSubscribed(vehicleID string) {
	m.mu.Lock()
	if _, exists := m.active[vehicleID]; exists {
		m.mu.Unlock()
		return
	}
	if m.parent == nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.active[vehicleID] = cancel
	m.mu.Unlock()

	go m.run(ctx, vehicleID)
}

// run is the per-vehicle subscription goroutine. It blocks inside
// SubscribeVehicleState, which internally reconnects with backoff on
// transient errors, and only returns when ctx is cancelled or the
// server rejects the session token.
func (m *StateMonitor) run(ctx context.Context, vehicleID string) {
	m.logger.Info("rivian ws subscribe", "vehicle", vehicleID)

	// Seed the cache from REST before the subscription starts
	// streaming. Rivian's subscription pushes deltas, so if we don't
	// establish a baseline the cache only ever contains whichever
	// handful of fields happened to change since connect — the rest
	// render as em-dashes in the UI. A REST GetVehicleState fills
	// odometer, gear, lat/lon, charger_state, etc. so mergeState has
	// something to overlay the deltas onto. Tire pressures (bar) and
	// other subscription-only fields stay zero here and get filled
	// in once the first push arrives.
	if st, err := m.client.State(ctx, vehicleID); err == nil && st != nil {
		m.mu.Lock()
		if m.cache[vehicleID] == nil {
			m.cache[vehicleID] = st
			m.stamp[vehicleID] = time.Now()
		} else {
			// A push may have raced us here; fold REST under it.
			m.cache[vehicleID] = mergeState(st, m.cache[vehicleID])
		}
		m.mu.Unlock()
	} else if err != nil && ctx.Err() == nil {
		m.logger.Warn("rivian rest seed failed", "vehicle", vehicleID, "err", err.Error())
	}

	// Periodic REST refresh: Rivian's subscription only pushes fields
	// that *change*, and it doesn't replay static fields (odometer,
	// gear while parked, charge limit, lat/lon for a parked vehicle)
	// on reconnect. If the initial REST seed happened while the car
	// was asleep, those fields can come back null and remain zero in
	// the cache indefinitely. Re-pulling REST every few minutes and
	// merging it *under* the WS state (WS wins on overlap) keeps
	// live delta freshness while backfilling anything Rivian dropped.
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	defer cancelRefresh()
	go m.periodicRefresh(refreshCtx, vehicleID, 2*time.Minute)

	err := m.client.SubscribeVehicleState(ctx, vehicleID, func(st *State) {
		m.mu.Lock()
		// Rivian pushes deltas — each frame contains only the
		// fields that changed. Merge non-zero/non-empty values
		// from the push over whatever we had cached so static
		// fields (odometer, gear, charge limit, tire pressures)
		// don't disappear between frames.
		m.cache[vehicleID] = mergeState(m.cache[vehicleID], st)
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
	})
	m.mu.Lock()
	delete(m.active, vehicleID)
	m.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		m.logger.Warn("rivian ws subscribe ended", "vehicle", vehicleID, "err", err.Error())
	}
}

// periodicRefresh pulls a fresh REST snapshot at interval and folds
// it *under* whatever is cached — subscription deltas always win on
// overlap, but REST fills in fields the subscription never pushes
// for a parked/sleeping car (odometer, charge limit, etc.). Bails on
// ctx cancellation.
func (m *StateMonitor) periodicRefresh(ctx context.Context, vehicleID string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, err := m.client.State(ctx, vehicleID)
			if err != nil {
				if ctx.Err() == nil {
					m.logger.Debug("rivian rest refresh failed", "vehicle", vehicleID, "err", err.Error())
				}
				continue
			}
			if st == nil {
				continue
			}
			m.mu.Lock()
			if m.cache[vehicleID] == nil {
				m.cache[vehicleID] = st
				m.stamp[vehicleID] = time.Now()
			} else {
				// mergeState(next=cached, prev=rest): cached values
				// (which include WS deltas) win over REST where both
				// are populated, REST fills in the zeros.
				m.cache[vehicleID] = mergeState(st, m.cache[vehicleID])
			}
			m.mu.Unlock()
		}
	}
}

// mergeState overlays fresh values from next onto prev. For each
// field: if next is non-zero / non-empty, it wins; otherwise we keep
// prev. Same pattern home-assistant-rivian uses in
// VehicleCoordinator._build_vehicle_info_dict.
//
// The `At` timestamp and `VehicleID` always come from next so a stale
// cache can't masquerade as fresh data.
func mergeState(prev, next *State) *State {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	out := *prev
	out.At = next.At
	out.VehicleID = next.VehicleID

	// Numerics: non-zero wins.
	mergeFloat(&out.BatteryLevelPct, next.BatteryLevelPct)
	mergeFloat(&out.DistanceToEmpty, next.DistanceToEmpty)
	mergeFloat(&out.OdometerKm, next.OdometerKm)
	mergeFloat(&out.ChargerPowerKW, next.ChargerPowerKW)
	mergeFloat(&out.ChargeTargetPct, next.ChargeTargetPct)
	mergeFloat(&out.Latitude, next.Latitude)
	mergeFloat(&out.Longitude, next.Longitude)
	mergeFloat(&out.SpeedKph, next.SpeedKph)
	mergeFloat(&out.HeadingDeg, next.HeadingDeg)
	mergeFloat(&out.AltitudeM, next.AltitudeM)
	mergeFloat(&out.CabinTempC, next.CabinTempC)
	mergeFloat(&out.OutsideTempC, next.OutsideTempC)
	mergeFloat(&out.OtaInstallProgress, next.OtaInstallProgress)
	mergeFloat(&out.TirePressureFLBar, next.TirePressureFLBar)
	mergeFloat(&out.TirePressureFRBar, next.TirePressureFRBar)
	mergeFloat(&out.TirePressureRLBar, next.TirePressureRLBar)
	mergeFloat(&out.TirePressureRRBar, next.TirePressureRRBar)

	// Strings: non-empty wins.
	mergeString(&out.Gear, next.Gear)
	mergeString(&out.DriveMode, next.DriveMode)
	mergeString(&out.ChargerState, next.ChargerState)
	mergeString(&out.ChargerStatus, next.ChargerStatus)
	mergeString(&out.ChargePortState, next.ChargePortState)
	mergeString(&out.RemoteChargingAvailable, next.RemoteChargingAvailable)
	mergeString(&out.CabinPreconditioningStatus, next.CabinPreconditioningStatus)
	mergeString(&out.PowerState, next.PowerState)
	mergeString(&out.AlarmSoundStatus, next.AlarmSoundStatus)
	mergeString(&out.TwelveVoltBatteryHealth, next.TwelveVoltBatteryHealth)
	mergeString(&out.WiperFluidState, next.WiperFluidState)
	mergeString(&out.OtaCurrentVersion, next.OtaCurrentVersion)
	mergeString(&out.OtaAvailableVersion, next.OtaAvailableVersion)
	mergeString(&out.OtaStatus, next.OtaStatus)
	mergeString(&out.TirePressureStatusFL, next.TirePressureStatusFL)
	mergeString(&out.TirePressureStatusFR, next.TirePressureStatusFR)
	mergeString(&out.TirePressureStatusRL, next.TirePressureStatusRL)
	mergeString(&out.TirePressureStatusRR, next.TirePressureStatusRR)

	// Booleans: the aggregate helpers (aggregateLocked, aggregateClosed)
	// default to true when every input is empty, so we can't
	// distinguish "really locked" from "nothing reported yet". Only
	// overwrite when the push actually carried door/lock fields —
	// detected by non-empty PowerState or ChargerStatus which co-
	// occur in real frames. This is a heuristic; the right fix is to
	// mark individual raw lock/close values in State and aggregate at
	// read time, but the heuristic holds for the frames Rivian
	// actually sends.
	if next.PowerState != "" || next.ChargerStatus != "" || next.Gear != "" {
		out.Locked = next.Locked
		out.DoorsClosed = next.DoorsClosed
		out.FrunkClosed = next.FrunkClosed
		out.LiftgateClosed = next.LiftgateClosed
		out.TailgateClosed = next.TailgateClosed
		out.TonneauClosed = next.TonneauClosed
	}
	return &out
}

func mergeFloat(dst *float64, src float64) {
	if src != 0 {
		*dst = src
	}
}

func mergeString(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

// Latest returns the most recently pushed state for a vehicle, along
// with the wall-clock time it was received, or (nil, zero) if nothing
// has arrived yet.
func (m *StateMonitor) Latest(vehicleID string) (*State, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cache[vehicleID], m.stamp[vehicleID]
}

// Prime stores a state from an out-of-band source (typically a REST
// fallback on first request) so subsequent Latest() calls return it
// immediately while the subscription is still spinning up.
func (m *StateMonitor) Prime(vehicleID string, st *State) {
	if st == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[vehicleID] = st
	m.stamp[vehicleID] = time.Now()
}

// discardWriter is an io.Writer that eats everything, used as a
// default slog sink when no logger is supplied.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
