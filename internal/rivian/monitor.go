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

	// priceLookup returns the operator-configured home $/kWh rate
	// and currency at the time of call. Consulted by the recorder
	// when persisting a charge whose Rivian-reported price is absent
	// (every home AC / L2 session). Nil means "don't snapshot a
	// cost" — the charge row lands with Cost=0 and the read-path
	// decorator computes an estimate instead.
	priceLookup PriceLookup
}

// PriceLookup returns the current home electricity rate and its
// ISO-4217 currency code. Rate of 0 means "not configured"; callers
// should leave the persisted cost at zero in that case so it doesn't
// show up as a misleading $0.00 in history.
type PriceLookup func() (ratePerKWh float64, currency string)

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

// SetPriceLookup wires a callback the recorder uses to stamp each
// closed charge with the current home $/kWh rate. Safe to call at
// any time; a nil value disables cost snapshotting.
func (m *StateMonitor) SetPriceLookup(fn PriceLookup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.priceLookup = fn
}

// Start binds the monitor to a parent context. All subscriptions
// started via EnsureSubscribed use a child context derived from this
// parent; cancelling parent tears them all down.
func (m *StateMonitor) Start(ctx context.Context) {
	m.mu.Lock()
	m.parent = ctx
	m.mu.Unlock()

	// Periodic janitor: close any live charge row left open from a
	// previous process death. The in-memory gear/charge mutex and
	// staleness guards in record() handle live cases, but neither
	// helps a row whose process died and never came back. Runs every
	// 10 min, marks anything still 'charging_*' with ended_at older
	// than 1h as 'abandoned'.
	if m.chargesStore != nil {
		go m.runStaleChargeJanitor(ctx)
	}
}

// runStaleChargeJanitor sweeps abandoned live charge rows on a timer.
// Errors are logged and swallowed — this is best-effort cleanup.
func (m *StateMonitor) runStaleChargeJanitor(ctx context.Context) {
	const sweepInterval = 10 * time.Minute
	const staleAfter = 1 * time.Hour

	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	// Run once immediately so a freshly-restarted process doesn't
	// leave stale rows visible for up to sweepInterval.
	m.sweepStaleCharges(ctx, staleAfter)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweepStaleCharges(ctx, staleAfter)
		}
	}
}

func (m *StateMonitor) sweepStaleCharges(ctx context.Context, staleAfter time.Duration) {
	if m.chargesStore == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := m.chargesStore.CloseStaleOpenLiveBefore(cctx, time.Now().Add(-staleAfter))
	if err != nil {
		m.logger.Debug("stale charge janitor failed", "err", err.Error())
		return
	}
	if n > 0 {
		m.logger.Info("stale charge janitor closed rows", "count", n)
	}
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
	// the cache indefinitely. Re-pulling REST periodically and
	// merging it *under* the WS state (WS wins on overlap) keeps
	// live delta freshness while backfilling anything Rivian dropped.
	//
	// Cadence is power-state-aware to avoid keeping a sleeping car
	// awake: see adaptiveRefreshInterval. home-assistant-rivian
	// refuses to REST-poll vehicleState at all for this reason.
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	defer cancelRefresh()
	go m.periodicRefresh(refreshCtx, vehicleID)
	go m.chargingSessionMetadataFetcher(refreshCtx, vehicleID)
	go m.chargingSessionSubscriber(refreshCtx, vehicleID)

	// Resubscribe loop: SubscribeVehicleState has per-connection
	// retry/backoff internally, but eventually returns (e.g. Rivian
	// idles the session when the car goes to sleep for a while).
	// Before v0.5.1 we exited after a single return, so a car that
	// slept overnight would wake with no live subscription — drives
	// went unrecorded until the UI happened to poll /api/vehicles.
	// Now we wrap the call in a loop that keeps resubscribing with
	// bounded exponential backoff until ctx is cancelled, which is
	// the only "done" signal we respect.
	//
	// A watchdog goroutine also cancels the per-subscribe context
	// when no push has landed for wsStaleThreshold, catching the
	// zombie-socket case where Rivian stops emitting frames but
	// never closes the WebSocket. Home Assistant does the same with
	// a 15-min heartbeat check.
	backoff := time.Second
	for ctx.Err() == nil {
		subCtx, cancelSub := context.WithCancel(ctx)
		// Reset stamp cursor so the watchdog measures this attempt,
		// not a stale value from a prior failed connection.
		m.mu.Lock()
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
		go m.watchSubscription(subCtx, vehicleID, cancelSub)

		err := m.client.SubscribeVehicleState(subCtx, vehicleID, func(st *State) {
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
		cancelSub()
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			m.logger.Warn("rivian ws subscribe ended, retrying", "vehicle", vehicleID, "err", err.Error(), "backoff", backoff.String())
		} else {
			m.logger.Info("rivian ws subscribe returned cleanly, resubscribing", "vehicle", vehicleID, "backoff", backoff.String())
		}
		// Any successful session resets backoff; a fast failure
		// escalates up to 60 s.
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
	m.mu.Lock()
	delete(m.active, vehicleID)
	m.mu.Unlock()
}

// wsStaleThreshold is how long we tolerate no WS pushes before
// assuming the socket has gone zombie and forcing a resubscribe.
// Rivian pushes at least powerState / chargerStatus heartbeats every
// few minutes on a live session; 10 min is comfortably above normal
// quiet periods for a sleeping car (which also gets periodic status
// frames) while still recovering quickly from a silent dropout.
const wsStaleThreshold = 10 * time.Minute

// watchSubscription force-cancels the per-subscribe context if no
// push has landed for wsStaleThreshold. Exits when its context is
// cancelled by the subscribe loop (normal completion) or when it
// fires the cancel itself (zombie detected). Safe to call cancel()
// multiple times — context.CancelFunc is idempotent.
func (m *StateMonitor) watchSubscription(ctx context.Context, vehicleID string, cancel context.CancelFunc) {
	t := time.NewTicker(wsStaleThreshold / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.RLock()
			last := m.stamp[vehicleID]
			m.mu.RUnlock()
			if !last.IsZero() && now.Sub(last) > wsStaleThreshold {
				m.logger.Warn("rivian ws stale, forcing resubscribe",
					"vehicle", vehicleID,
					"since_last_push", now.Sub(last).Round(time.Second).String())
				cancel()
				return
			}
		}
	}
}

// adaptiveRefreshInterval picks a REST refresh cadence based on the
// cached power state. REST GetVehicleState is known to bump the
// vehicle out of deep sleep in some firmwares (home-assistant-rivian
// explicitly refuses to poll vehicleState for this reason), so we
// only poll aggressively when the car is demonstrably awake. The WS
// subscription keeps running at all power states; REST is only a
// backfill for fields Rivian's subscription doesn't replay.
//
//	asleep / no cache: 30 min — minimal wake pressure, we have WS
//	standby / ready:   10 min — car is awake anyway
//	go / charging:      2 min — driving or charging, we want freshness
func (m *StateMonitor) adaptiveRefreshInterval(vehicleID string) time.Duration {
	m.mu.RLock()
	st := m.cache[vehicleID]
	m.mu.RUnlock()
	if st == nil {
		return 30 * time.Minute
	}
	switch strings.ToLower(st.PowerState) {
	case "go":
		return 2 * time.Minute
	case "ready", "standby":
		return 10 * time.Minute
	case "sleep", "":
		// Sleeping or unknown. If the car is charging we still want
		// responsiveness, so key off chargerState too.
		if strings.Contains(strings.ToLower(st.ChargerState), "charging") {
			return 2 * time.Minute
		}
		return 30 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// periodicRefresh pulls a fresh REST snapshot on an adaptive cadence
// and folds it *under* whatever is cached — subscription deltas
// always win on overlap, but REST fills in fields the subscription
// never pushes for a parked/sleeping car (odometer, charge limit,
// etc.). Interval is recomputed each tick from the current cached
// powerState so a sleeping car doesn't get poked every 2 minutes.
// Bails on ctx cancellation.
func (m *StateMonitor) periodicRefresh(ctx context.Context, vehicleID string) {
	for {
		interval := m.adaptiveRefreshInterval(vehicleID)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
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

// chargingSessionMetadataFetcher pulls session-immutable metadata
// (IsRivianCharger, StartTime) from Rivian's REST getLiveSessionHistory
// endpoint exactly once per charging session. Live telemetry (power,
// energy, SoC, time/range) comes via the Parallax and ChargingSession
// WS subscriptions, so repeated REST polling is wasted work and — for
// home-AC / L2 sessions where REST returns zero-filled bodies — risks
// clobbering WS values on race.
//
// Lifecycle: watches charger_state; on transition into an active
// session fetches REST once and merges via applyLiveSession; on
// transition out, resets so the next session gets a fresh fetch.
func (m *StateMonitor) chargingSessionMetadataFetcher(ctx context.Context, vehicleID string) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	fetched := false
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
			charging := cs == "charging_active" || cs == "charging_connecting"
			if !charging {
				fetched = false
				continue
			}
			if fetched {
				continue
			}
			sess, err := m.client.LiveSession(ctx, vehicleID)
			if err != nil {
				if ctx.Err() == nil {
					m.logger.Debug("rivian live-session fetch failed", "vehicle", vehicleID, "err", err.Error())
				}
				continue
			}
			if sess == nil {
				continue
			}
			// Mark fetched even when sess.Active is false — REST
			// reports active=false for home-AC sessions, but the
			// StartTime / IsRivianCharger fields are still usable.
			fetched = true
			m.applyLiveSession(ctx, vehicleID, sess)
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
	// Check charging state every 5s. Cheap — just reads the cache.
	// The previous 15s interval added noticeable lag between plugging
	// in and the WS opening; 5s is still cheap and matches how often
	// vehicleState pushes arrive while charging.
	t := time.NewTicker(5 * time.Second)
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
		// Reuse the recorder's charging-state predicate so we open the
		// subscription for every state the rest of the app considers
		// "the car is charging" — charging_ready, waiting_on_charger,
		// charging_active, charging_connecting, etc. The previous
		// explicit list missed charging_ready, which is the state a
		// just-plugged home AC session spends its first few seconds in
		// before transitioning to charging_active. That meant on a
		// fresh plug-in the subscription never opened at all.
		return isChargingCS(st.ChargerState)
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
				m.mu.RLock()
				st := m.cache[vehicleID]
				m.mu.RUnlock()
				csLog := ""
				if st != nil {
					csLog = st.ChargerState
				}
				m.logger.Info("rivian charging-session ws starting",
					"vehicle", vehicleID, "charger_state", csLog)
				// Both subscriptions run on the shared WS mux (see
				// ws_mux.go). ChargingSession gives us price /
				// currency / chart buckets (Rivian-EVSE only);
				// Parallax gives us real power + energy for every
				// session type including home AC. Running both in
				// parallel is now safe — the mux puts them on a
				// single connection, avoiding Rivian's concurrent-
				// connection rejection.
				go func() {
					firstLogged := false
					err := m.client.SubscribeChargingSession(subCtx, vehicleID, func(sess *LiveSession) {
						if sess == nil {
							return
						}
						if !firstLogged {
							m.logger.Info("rivian charging-session ws first frame",
								"vehicle", vehicleID,
								"power_kw", sess.PowerKW,
								"energy_kwh", sess.TotalChargedEnergyKWh,
								"elapsed_s", sess.TimeElapsedSeconds,
								"charger_state", sess.VehicleChargerState)
							firstLogged = true
						}
						m.applyLiveSession(ctx, vehicleID, sess)
					})
					if err != nil && subCtx.Err() == nil {
						m.logger.Warn("rivian charging-session ws ended",
							"vehicle", vehicleID, "err", err.Error())
					}
				}()
				go func() {
					firstLogged := false
					err := m.client.SubscribeParallaxCharging(subCtx, vehicleID, func(sess *LiveSession) {
						if sess == nil {
							return
						}
						if !firstLogged {
							m.logger.Info("rivian parallax charge-breakdown first frame",
								"vehicle", vehicleID,
								"power_kw", sess.PowerKW,
								"energy_kwh", sess.TotalChargedEnergyKWh,
								"elapsed_s", sess.TimeElapsedSeconds)
							firstLogged = true
						}
						m.applyLiveSession(ctx, vehicleID, sess)
					})
					if err != nil && subCtx.Err() == nil {
						m.logger.Warn("rivian parallax ws ended",
							"vehicle", vehicleID, "err", err.Error())
					}
				}()
			} else if !want && subActive {
				m.logger.Info("rivian charging-session ws stopping",
					"vehicle", vehicleID)
				stop()
			}
		}
	}
}

// applyLiveSession merges a pushed LiveSession into m.lastSession
// (preserving non-zero fields from the previous snapshot so
// concurrent ChargingSession + Parallax subscribers don't clobber
// each other), updates cache.ChargerPowerKW, and triggers a recorder
// pass. Shared by both the ChargingSession and Parallax subscribers.
func (m *StateMonitor) applyLiveSession(ctx context.Context, vehicleID string, sess *LiveSession) {
	m.mu.Lock()
	if prev := m.lastSession[vehicleID]; prev != nil {
		// Preserve IsRivianCharger once any source has reported it.
		// Only the REST poller selects this field; WS subscribers
		// leave it false, so we keep prev's true value across pushes.
		if prev.IsRivianCharger {
			sess.IsRivianCharger = true
		}
		// Preserve Active once any source has set it true. The REST
		// one-shot reports Active=false for home-AC sessions; without
		// this guard it would briefly flip the WS-observed state to
		// inactive.
		if prev.Active {
			sess.Active = true
		}
		// Field-level fallback: if this push reports zero for a
		// field the prior snapshot populated, keep the prior value.
		// Lets the Parallax + ChargingSession streams complement
		// each other without overwriting known values with zeros.
		if sess.PowerKW == 0 {
			sess.PowerKW = prev.PowerKW
		}
		if sess.TotalChargedEnergyKWh == 0 {
			sess.TotalChargedEnergyKWh = prev.TotalChargedEnergyKWh
		}
		if sess.RangeAddedKm == 0 {
			sess.RangeAddedKm = prev.RangeAddedKm
		}
		if sess.KilometersChargedPerHour == 0 {
			sess.KilometersChargedPerHour = prev.KilometersChargedPerHour
		}
		if sess.TimeElapsedSeconds == 0 {
			sess.TimeElapsedSeconds = prev.TimeElapsedSeconds
		}
		if sess.TimeRemainingSeconds == 0 {
			sess.TimeRemainingSeconds = prev.TimeRemainingSeconds
		}
		if sess.SoCPct == 0 {
			sess.SoCPct = prev.SoCPct
		}
		if sess.VehicleChargerState == "" {
			sess.VehicleChargerState = prev.VehicleChargerState
		}
		if sess.StartTime == "" {
			sess.StartTime = prev.StartTime
		}
		if sess.CurrentPrice == "" {
			sess.CurrentPrice = prev.CurrentPrice
		}
		if sess.CurrentCurrency == "" {
			sess.CurrentCurrency = prev.CurrentCurrency
		}
		// Parallax-only breakdown fields. The regular ChargingSession
		// stream leaves these at zero, so we always preserve the
		// last-known Parallax value across non-Parallax pushes.
		if sess.PackKWh == 0 {
			sess.PackKWh = prev.PackKWh
		}
		if sess.ThermalKWh == 0 {
			sess.ThermalKWh = prev.ThermalKWh
		}
		if sess.OutletsKWh == 0 {
			sess.OutletsKWh = prev.OutletsKWh
		}
		if sess.SystemKWh == 0 {
			sess.SystemKWh = prev.SystemKWh
		}
	}
	m.lastSession[vehicleID] = sess
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
	mergeFloat(&out.BatteryCapacityKWh, next.BatteryCapacityKWh)
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

// LatestLiveSession returns the last charging-session snapshot
// observed for the vehicle, whichever source got there first — the
// WebSocket ChargingSession subscription or the REST
// getLiveSessionHistory poller. Callers should treat the result as
// read-only; it may be nil if no session has ever been seen for
// this vehicle.
func (m *StateMonitor) LatestLiveSession(vehicleID string) *LiveSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastSession[vehicleID]
}

// LiveDrive is the wire-friendly snapshot of an in-flight drive
// session — the recorder's internal `liveDrive` accumulator
// projected into the same style that /api/live-session returns for
// charges. Returned by ActiveDrive; fields are flat so the frontend
// can render without having to reach into recorder internals.
type LiveDrive struct {
	VehicleID       string    `json:"vehicle_id"`
	Number          int64     `json:"number"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	ElapsedSec      float64   `json:"elapsed_sec"`
	StartSoCPct     float64   `json:"start_soc_pct"`
	EndSoCPct       float64   `json:"end_soc_pct"`
	SoCUsedPct      float64   `json:"soc_used_pct"`
	StartOdometerMi float64   `json:"start_odometer_mi"`
	EndOdometerMi   float64   `json:"end_odometer_mi"`
	DistanceMi      float64   `json:"distance_mi"`
	MaxSpeedMph     float64   `json:"max_speed_mph"`
	AvgSpeedMph     float64   `json:"avg_speed_mph"`
	EnergyUsedKWh   float64   `json:"energy_used_kwh"`
	MiPerKWh        float64   `json:"mi_per_kwh"`
	PackKWh         float64   `json:"pack_kwh"`
}

// ActiveDrive returns a snapshot of the in-flight drive session for
// the given vehicle, or nil if no drive is currently open. The
// snapshot is derived from the recorder's accumulator under sessMu
// so callers see a consistent view even while telemetry frames are
// being folded in on another goroutine. Energy and efficiency are
// computed from the SoC delta × per-vehicle pack size — the same
// fallback the charge recorder uses for home-AC sessions.
func (m *StateMonitor) ActiveDrive(vehicleID string) *LiveDrive {
	m.sessMu.Lock()
	sess := m.sessions[vehicleID]
	if sess == nil || sess.drive == nil {
		m.sessMu.Unlock()
		return nil
	}
	d := *sess.drive // shallow copy so we can release the lock before math
	m.sessMu.Unlock()

	pack := m.PackKWhFor(vehicleID)
	elapsed := d.endAt.Sub(d.startedAt).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	distance := d.endOdoMi - d.startOdoMi
	if distance < 0 {
		distance = 0
	}
	socUsed := d.startSoC - d.endSoC
	if socUsed < 0 {
		socUsed = 0
	}
	avg := 0.0
	if d.speedN > 0 {
		avg = d.sumSpeed / float64(d.speedN)
	}
	var energy, mipk float64
	if pack > 0 && socUsed > 0 {
		energy = socUsed / 100.0 * pack
		if distance > 0 {
			mipk = distance / energy
		}
	}
	return &LiveDrive{
		VehicleID:       vehicleID,
		Number:          d.number,
		StartedAt:       d.startedAt,
		EndedAt:         d.endAt,
		ElapsedSec:      elapsed,
		StartSoCPct:     d.startSoC,
		EndSoCPct:       d.endSoC,
		SoCUsedPct:      socUsed,
		StartOdometerMi: d.startOdoMi,
		EndOdometerMi:   d.endOdoMi,
		DistanceMi:      distance,
		MaxSpeedMph:     d.maxSpeed,
		AvgSpeedMph:     avg,
		EnergyUsedKWh:   energy,
		MiPerKWh:        mipk,
		PackKWh:         pack,
	}
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
	// image endpoint is down or returns 0 images. Rivian hands back
	// a handful of configurator-rendered angles per vehicle; we keep
	// all of them for the gallery and pick one hero for the header.
	imagesByVehicle := map[string][]VehicleImage{}
	heroByVehicle := map[string]string{}
	if images, ierr := m.client.VehicleImages(ctx); ierr == nil {
		for _, img := range images {
			if img.VehicleID == "" || img.URL == "" {
				continue
			}
			imagesByVehicle[img.VehicleID] = append(imagesByVehicle[img.VehicleID], img)
		}
		for vid, list := range imagesByVehicle {
			heroByVehicle[vid] = pickHeroImage(list)
		}
	} else {
		m.logger.Warn("vehicle images fetch failed", "err", ierr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range vehicles {
		v := vehicles[i]
		if url, ok := heroByVehicle[v.ID]; ok {
			v.ImageURL = url
		}
		if list, ok := imagesByVehicle[v.ID]; ok {
			v.Images = list
		}
		m.vehicleInfo[v.ID] = &v
	}
	return nil
}

// pickHeroImage chooses the best image to use as the single
// header / card illustration. Rivian's placement tags look like
// `side-exterior-3qfront-driver`, `side-exterior-3qrear-driver`,
// `front-exterior`, `interior-cabin-driver`, etc. A 3/4 front shot
// from the driver side is the classic marketing hero, so we score
// entries and pick the highest. Falls back to the first image when
// no placement hints match.
func pickHeroImage(list []VehicleImage) string {
	if len(list) == 0 {
		return ""
	}
	best, bestScore := list[0].URL, -1
	for _, img := range list {
		p := strings.ToLower(img.Placement)
		score := 0
		switch {
		case strings.Contains(p, "3qfront"), strings.Contains(p, "3q-front"):
			score = 10
		case strings.Contains(p, "3qrear"), strings.Contains(p, "3q-rear"):
			score = 7
		case strings.Contains(p, "side") && strings.Contains(p, "exterior"):
			score = 6
		case strings.Contains(p, "front") && strings.Contains(p, "exterior"):
			score = 5
		case strings.Contains(p, "rear") && strings.Contains(p, "exterior"):
			score = 4
		case strings.Contains(p, "exterior"):
			score = 3
		case strings.Contains(p, "interior"):
			score = 1
		}
		// Prefer driver-side over passenger-side when both are present.
		if strings.Contains(p, "driver") {
			score++
		}
		if score > bestScore {
			best, bestScore = img.URL, score
		}
	}
	return best
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

// observeBatteryCapacity folds a vehicle-reported usable pack
// capacity (batteryCapacity from vehicleState) into the in-memory
// vehicleInfo cache. The live number is authoritative — it reflects
// the actual pack in the car, including any degradation or recall
// rebalancing — whereas the bootstrap value from InferPackKWh is a
// lookup-table guess by model/trim/year. No-op when kwh <= 0 or
// equal to the cached value.
func (m *StateMonitor) observeBatteryCapacity(vehicleID string, kwh float64) {
	if kwh <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.vehicleInfo[vehicleID]
	if v == nil {
		// First touch — create a stub so PackKWhFor can see the value
		// even before RefreshVehicleInfo has run. Other fields stay
		// zero/empty and get filled in by the later refresh.
		m.vehicleInfo[vehicleID] = &Vehicle{ID: vehicleID, PackKWh: kwh}
		return
	}
	if v.PackKWh == kwh {
		return
	}
	cp := *v
	cp.PackKWh = kwh
	m.vehicleInfo[vehicleID] = &cp
}
