package rivian

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
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

	// Live recording stores (all optional — nil stores disable that
	// particular writer). Samples captures every merged state update
	// as a row in vehicle_state. Drives and charges capture derived
	// sessions on gear/chargerState transitions.
	samplesStore *samples.Store
	drivesStore  *drives.Store
	chargesStore *charges.Store

	// Per-vehicle in-flight session accumulators, keyed by vehicleID.
	// Access guarded by sessMu. Separate from mu so recorder work
	// doesn't serialize behind cache reads.
	sessMu   sync.Mutex
	sessions map[string]*liveSessions

	// Latest LiveSession payload per vehicle, refreshed by
	// chargingSessionPoller. Used by the recorder to enrich charge
	// rows with TotalChargedEnergyKWh / RangeAddedKm. Guarded by mu
	// alongside the state cache.
	lastSession map[string]*LiveSession

	// Per-vehicle metadata (model/trim/pack/image), fetched once at
	// startup via RefreshVehicleInfo. Consulted by the recorder to
	// pick an accurate pack size for the SoC-delta energy fallback.
	// Guarded by mu alongside the rest of the cache.
	vehicleInfo map[string]*Vehicle
}

// NewStateMonitor wraps a live client. Pass a logger (usually from
// main.go's structured logger). nil is allowed; events will be
// discarded.
func NewStateMonitor(client *LiveClient, logger *slog.Logger) *StateMonitor {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return &StateMonitor{
		client:      client,
		logger:      logger,
		cache:       make(map[string]*State),
		stamp:       make(map[string]time.Time),
		active:      make(map[string]context.CancelFunc),
		sessions:    make(map[string]*liveSessions),
		lastSession: make(map[string]*LiveSession),
		vehicleInfo: make(map[string]*Vehicle),
	}
}

// SetStores wires the recording stores. All three are optional — pass
// nil to disable that particular writer. Safe to call before Start;
// racy if called after subscriptions are running.
func (m *StateMonitor) SetStores(samplesStore *samples.Store, drivesStore *drives.Store, chargesStore *charges.Store) {
	m.samplesStore = samplesStore
	m.drivesStore = drivesStore
	m.chargesStore = chargesStore
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
		var merged *State
		prev := m.cache[vehicleID]
		if prev == nil {
			merged = st
		} else {
			// A push may have raced us here; fold REST under it.
			merged = mergeState(st, prev)
		}
		m.cache[vehicleID] = merged
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
		m.record(ctx, vehicleID, prev, merged)
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
	go m.chargingSessionPoller(refreshCtx, vehicleID, 30*time.Second)
	go m.chargingSessionSubscriber(refreshCtx, vehicleID)

	err := m.client.SubscribeVehicleState(ctx, vehicleID, func(st *State) {
		m.mu.Lock()
		// Rivian pushes deltas — each frame contains only the
		// fields that changed. Merge non-zero/non-empty values
		// from the push over whatever we had cached so static
		// fields (odometer, gear, charge limit, tire pressures)
		// don't disappear between frames.
		prev := m.cache[vehicleID]
		merged := mergeState(prev, st)
		m.cache[vehicleID] = merged
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
		m.record(ctx, vehicleID, prev, merged)
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
			var merged *State
			prev := m.cache[vehicleID]
			if prev == nil {
				merged = st
			} else {
				// mergeState(next=cached, prev=rest): cached values
				// (which include WS deltas) win over REST where both
				// are populated, REST fills in the zeros.
				merged = mergeState(st, prev)
			}
			m.cache[vehicleID] = merged
			m.stamp[vehicleID] = time.Now()
			m.mu.Unlock()
			m.record(ctx, vehicleID, prev, merged)
		}
	}
}

// chargingSessionPoller hits Rivian's chrg/user/graphql endpoint at
// interval to pull real-time charging metrics (kW, SoC, minutes
// remaining, range added). Only runs when the cached state reports
// an active charging session — we don't spam the endpoint when
// the car is parked offline.
//
// The result's PowerKW is merged into State.ChargerPowerKW so the
// existing /api/state endpoint can render it without a second round
// trip. Also cached separately for /api/live-session/:id callers.
func (m *StateMonitor) chargingSessionPoller(ctx context.Context, vehicleID string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.RLock()
			st := m.cache[vehicleID]
			m.mu.RUnlock()
			if st == nil {
				continue
			}
			cs := strings.ToLower(strings.TrimSpace(st.ChargerState))
			if cs != "charging_active" && cs != "charging_connecting" {
				continue
			}
			sess, err := m.client.LiveSession(ctx, vehicleID)
			if err != nil {
				if ctx.Err() == nil {
					m.logger.Debug("rivian live-session poll failed", "vehicle", vehicleID, "err", err.Error())
				}
				continue
			}
			if sess == nil || !sess.Active {
				continue
			}
			m.mu.Lock()
			var merged *State
			prev := m.cache[vehicleID]
			m.lastSession[vehicleID] = sess
			if cur := prev; cur != nil && sess.PowerKW > 0 {
				cp := *cur
				cp.ChargerPowerKW = sess.PowerKW
				merged = &cp
				m.cache[vehicleID] = merged
				m.stamp[vehicleID] = time.Now()
			}
			m.mu.Unlock()
			if merged != nil {
				m.record(ctx, vehicleID, prev, merged)
			}
		}
	}
}

// chargingSessionSubscriber runs a WebSocket ChargingSession
// subscription whenever the cached charger_state indicates an active
// session (charging_active, charging_connecting, charging_complete).
// Unlike the REST getLiveSessionHistory endpoint — which returns
// active:false with zeroed payload for L1 / L2 / home AC — this
// subscription pushes real telemetry (power, energy delivered, time
// elapsed/remaining, range added) for every session type the vehicle
// reports, matching what the Rivian mobile app shows.
//
// The subscription is started on charger_state transitions and torn
// down via ctx cancellation when charging ends. Pushed frames are
// merged into m.lastSession so /api/live-session/:id returns the
// subscription's data preferentially.
func (m *StateMonitor) chargingSessionSubscriber(ctx context.Context, vehicleID string) {
	// Check charging state every 15s. Cheap — just reads the cache.
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()

	var (
		subCancel context.CancelFunc
		subActive bool
	)
	stop := func() {
		if subCancel != nil {
			subCancel()
			subCancel = nil
		}
		subActive = false
	}
	defer stop()

	isCharging := func() bool {
		m.mu.RLock()
		st := m.cache[vehicleID]
		m.mu.RUnlock()
		if st == nil {
			return false
		}
		cs := strings.ToLower(strings.TrimSpace(st.ChargerState))
		// Keep the subscription open through the whole session
		// including the tail-end "charging_complete" state so we
		// see the final energy/range totals pushed out.
		return cs == "charging_active" ||
			cs == "charging_connecting" ||
			cs == "charging_complete"
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			want := isCharging()
			if want && !subActive {
				subCtx, cancel := context.WithCancel(ctx)
				subCancel = cancel
				subActive = true
				go func() {
					err := m.client.SubscribeChargingSession(subCtx, vehicleID, func(sess *LiveSession) {
						if sess == nil {
							return
						}
						m.mu.Lock()
						// Preserve IsRivianCharger from the REST poller
						// (the subscription doesn't select it).
						if prev := m.lastSession[vehicleID]; prev != nil {
							sess.IsRivianCharger = prev.IsRivianCharger
						}
						m.lastSession[vehicleID] = sess
						// Feed the latest power into the cached
						// state so /api/state and the open-charge
						// row reflect subscription pushes. Even when
						// powerKW momentarily reports 0 mid-session
						// we still want to trigger a recorder pass
						// so the open live-charge row gets its
						// running energy / range totals refreshed
						// from lastSession on each push.
						prev := m.cache[vehicleID]
						var merged *State
						if prev != nil {
							cp := *prev
							if sess.PowerKW > 0 {
								cp.ChargerPowerKW = sess.PowerKW
							}
							merged = &cp
							m.cache[vehicleID] = merged
							m.stamp[vehicleID] = time.Now()
						}
						m.mu.Unlock()
						if merged != nil {
							m.record(ctx, vehicleID, prev, merged)
						}
					})
					if err != nil && subCtx.Err() == nil {
						m.logger.Debug("rivian charging-session ws ended",
							"vehicle", vehicleID, "err", err.Error())
					}
				}()
			} else if !want && subActive {
				stop()
			}
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

// RefreshVehicleInfo pulls the vehicles list + configurator images
// from Rivian's gateway and caches a per-vehicle metadata record
// including model, trim, inferred pack kWh, and a 3/4 image URL.
// Called once at startup (best-effort); errors are returned to the
// caller so they can log but shouldn't be fatal. Missing images are
// not an error — PackKWh still gets populated from the vehicles
// query.
func (m *StateMonitor) RefreshVehicleInfo(ctx context.Context) error {
	vehicles, err := m.client.Vehicles(ctx)
	if err != nil {
		return err
	}
	// Best-effort image fetch — don't fail the whole refresh if the
	// image endpoint is down or returns 0 images.
	imagesByVehicle := map[string]string{}
	if images, ierr := m.client.VehicleImages(ctx); ierr == nil {
		for _, img := range images {
			if img.VehicleID == "" || img.URL == "" {
				continue
			}
			// First image wins; the API returns multiple (interior /
			// exterior / variants) — we just want something to show.
			if _, seen := imagesByVehicle[img.VehicleID]; !seen {
				imagesByVehicle[img.VehicleID] = img.URL
			}
		}
	} else {
		m.logger.Warn("vehicle images fetch failed", "err", ierr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range vehicles {
		v := vehicles[i]
		if url, ok := imagesByVehicle[v.ID]; ok {
			v.ImageURL = url
		}
		m.vehicleInfo[v.ID] = &v
	}
	return nil
}

// VehicleInfo returns the cached per-vehicle metadata record, or nil
// if RefreshVehicleInfo hasn't been called (or hasn't seen this
// vehicle yet). The returned pointer is a copy; safe to read without
// holding the lock.
func (m *StateMonitor) VehicleInfo(vehicleID string) *Vehicle {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vehicleInfo[vehicleID]
	if !ok || v == nil {
		return nil
	}
	cp := *v
	return &cp
}

// AllVehicleInfo returns a snapshot of every cached vehicle record.
// Used by the HTTP /api/vehicles endpoint.
func (m *StateMonitor) AllVehicleInfo() []Vehicle {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Vehicle, 0, len(m.vehicleInfo))
	for _, v := range m.vehicleInfo {
		if v == nil {
			continue
		}
		out = append(out, *v)
	}
	return out
}

// PackKWhFor returns the best-known usable pack capacity for the
// vehicle, falling back to DefaultPackKWh when no metadata is
// cached. Used by the recorder's SoC-delta energy fallback.
func (m *StateMonitor) PackKWhFor(vehicleID string) float64 {
	v := m.VehicleInfo(vehicleID)
	if v == nil || v.PackKWh <= 0 {
		return DefaultPackKWh
	}
	return v.PackKWh
}
