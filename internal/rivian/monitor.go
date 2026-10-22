package rivian

import (
	"encoding/json"
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

	mu    sync.RWMutex
	cache map[string]*State
	stamp map[string]time.Time
	// sampleMu/lastSampleAt enforce minSampleSpacing on vehicle_state
	// writes. Separate from mu because every record() caller has
	// already released mu by the time it reaches the recorder, and we
	// must not re-take a lock the hot path just dropped.
	sampleMu     sync.Mutex
	// writeFails rate-limits the Warn line for failed live writes.
	writeFails writeFailureLog
	lastSampleAt map[string]time.Time
	// parked skips unchanged parked rows (parked_dedupe.go); parkedDedupe is
	// the switch.
	parked       parkedGate
	parkedDedupe bool
	// fallbackAt is when /api/state on this pod last fetched a vehicle
	// it doesn't stream straight from Rivian (ServeState), so that path
	// calls Rivian at most once per stateRefetchEvery.
	fallbackMu sync.Mutex
	fallbackAt map[string]time.Time

	// remoteLatest holds RemoteLatest's recent reads of the shared store
	// (remoteLatestTTL).
	remoteMu     sync.Mutex
	remoteLatest map[string]remoteLatestEntry
	// wsSeen[vehicleID] is true once we've received at least one WS
	// frame for that vehicle in this process's lifetime. Until then
	// adaptiveRefreshInterval polls REST aggressively (2 min) so a
	// pod that booted while the car was already driving can detect
	// the in-progress trip via REST instead of waiting up to 30 min
	// for the cached "charging_complete" PowerState to time out.
	// Cleared on Unsubscribe; survives WS reconnects within a single
	// process. Guarded by mu.
	wsSeen map[string]bool
	// wokeAt[vehicleID] is when this pod received the frame that took
	// the cached PowerState out of sleep: the wake attribution anchor
	// (our clock, like the action log's), which the sleep gate only
	// sees on its next tick. Written by putCacheLocked; guarded by mu.
	wokeAt map[string]time.Time
	active map[string]context.CancelFunc
	// activeCtx[vehicleID] is the context of the run goroutine that
	// active[vehicleID] cancels, so a goroutine exiting after a quick
	// release → re-acquire removes only its own entry, not its
	// successor's (see releaseActive).
	activeCtx map[string]context.Context
	// subCancel exposes the *current* SubscribeVehicleState ctx-cancel
	// to non-owning goroutines (notably periodicRefresh) so a REST
	// observation that the car just woke up can poke the WS to
	// resubscribe — instead of waiting up to wsStaleThreshold for the
	// watchdog to notice. Populated by run() before each Subscribe
	// call and cleared on return; nil/missing entry means the
	// supervisor is currently in backoff. Guarded by mu.
	subCancel map[string]context.CancelCauseFunc
	// subCtx[vehicleID] is the context subCancel[vehicleID] cancels, so a
	// subscription that ends after its successor started clears only its
	// own handle (clearSubCancel). Guarded by mu.
	subCtx map[string]context.Context
	// shortSubEnds[vehicleID] holds recent timestamps of abnormally
	// short-lived WS sessions. A burst of them means the live feed is
	// flapping — the classic signature of a *competing subscriber to the
	// same vehicle* (the same VIN connected from another Rivolt instance
	// / Rivian account), which kicks our session repeatedly. Guarded by mu.
	shortSubEnds   map[string][]time.Time
	lastFlapWarnAt map[string]time.Time
	parent         context.Context //nolint:containedctx // outer ctx for spawned subscriptions

	// parallaxGPS is the master switch (RIVOLT_PARALLAX_GPS) for the
	// Parallax gnss subscriber. It mirrors the app's Firebase
	// `parallaxCommand` remote-config layer: necessary but not
	// sufficient — a vehicle also has to advertise Parallax
	// connectivity (VEHICLE_CONNECTIVITY_PARALLAX=AVAILABLE). Env
	// defaults off; enabled in both prod and preview.
	//
	// Parallax gnss is the PRIMARY GPS backbone (steady ~60s cadence,
	// no multi-minute stalls); vehicleState's denser ~3s fixes fill
	// detail between gnss ticks but are demoted to that filler role by
	// the recorder's fix-timestamp monotonicity guard, because they
	// stall for minutes and replay stale fixes (observed 2026-07-14: a
	// 5-minute full blackout mid-drive).
	parallaxGPS bool
	// parallaxDriveDynamics (RIVOLT_PARALLAX_DRIVE_DYNAMICS) subscribes to
	// dynamics.vehicle.{gear,drive_mode,odometer} + vehicle.power.state,
	// shadow-logs each value next to vehicleState, and makes them
	// authoritative:
	//   - gear: a driving gear (R/N/D) opens/sustains the drive now, so it
	//     starts at the true departure (correct pre-drive odometer/location)
	//     instead of 60–90s late. One-way — Parallax P is never applied
	//     (it blips mid-drive during stops, observed 2026-07-14; closing on
	//     it would fragment the trip), so vehicleState stays the close
	//     authority.
	//   - odometer: advances the cache only when higher than vehicleState
	//     (monotonic stall-bridge; never lowers its finer 0.01-mi to km).
	//   - power.state / drive_mode: mapped enum applied to the cache.
	// Never advances m.stamp, so the vehicleState watchdog is untouched.
	// Off by default; on with RIVOLT_PARALLAX.
	parallaxDriveDynamics bool
	// parallaxCapture (RIVOLT_PARALLAX_CAPTURE) adds the not-yet-decoded
	// read topics (CaptureRVMs) to the drive-dynamics subscription for
	// raw-payload shadow RE. Preview-only — keeps prod off the
	// experimental topics until each is decoded and promoted. Requires
	// parallaxDriveDynamics (the subscription it rides on).
	parallaxCapture bool
	// lastParallaxAt[vehicleID] is when any Parallax topic (gnss,
	// battery_state, drive-dynamics, charging) last delivered a frame.
	// Feeds parallaxLivenessWatch — the observability precursor to the
	// Phase-5 watchdog that must exist before vehicleState can be dropped
	// as the fallback (a silently dead Parallax feed would otherwise lose
	// all telemetry invisibly). Guarded by mu.
	lastParallaxAt map[string]time.Time
	// parallaxSleepFrames[vehicleID] counts Parallax frames that arrived
	// while the cached powerState was "sleep", accumulated between
	// parallaxLivenessWatch ticks. Proves Parallax stays quiet while the
	// car sleeps (count should be 0) — a non-zero count means a topic is
	// still pushing during sleep, worth investigating for wake-keeping /
	// battery drain. Guarded by mu.
	parallaxSleepFrames map[string]int
	// lastTopicTs[vehicleID|topic] is the highest event ts_ms seen for a
	// Parallax topic, used to tell a genuinely new frame from a cloud
	// keepalive replay (which re-sends the last snapshot with a frozen
	// ts_ms). Only ts-advancing frames count toward parallaxSleepFrames.
	// Guarded by mu.
	lastTopicTs map[string]int64
	// lastPxGear[vehicleID] is the most recent mapped Parallax gear and
	// when it arrived. The recorder's drive-close debounce consults it:
	// a fresh Parallax P corroborates a close immediately, a fresh
	// Parallax R/N/D vetoes the fast close paths (vehicleState P blip).
	// Guarded by mu.
	lastPxGear   map[string]string
	lastPxGearAt map[string]time.Time
	// lastPxPower[vehicleID] is the last fresh Parallax power state
	// (mapped, e.g. "sleep"). Some vehicles (R2) never report sleep in
	// vehicleState, so the WS merge keeps a Parallax sleep against a
	// vehicleState "ready" while Parallax is live (holdParallaxLocked).
	// Guarded by mu.
	lastPxPower map[string]string
	// vehChargerStatus[vehicleID] is set once vehicleState has delivered a
	// chargerStatus (an R1). Parallax charging.session.status is applied
	// only to vehicles without it (an R2); on the rest it's shadow-logged
	// to validate the mapping. Guarded by mu.
	vehChargerStatus map[string]bool
	// vehOtaVersion[vehicleID] is set once vehicleState has delivered an
	// otaCurrentVersion. Parallax ota.deployment.state fills the versions
	// only for vehicles without it (an R2). Guarded by mu.
	vehOtaVersion map[string]bool
	// lastVehStateGPS[vehicleID] is when vehicleState last delivered a
	// GPS fix. The gnss subscriber injects a Parallax point only when
	// this is older than parallaxGPSStallThreshold, i.e. vehicleState
	// has stalled. Guarded by mu.
	lastVehStateGPS map[string]time.Time
	// lastVehStateSoC[vehicleID] is the most recent SoC% vehicleState
	// reported, stashed before Parallax may overwrite BatteryLevelPct in
	// the cache. Lets the battery_state subscriber log the Parallax-vs-
	// vehicleState SoC delta (Phase-3 energy reconciliation) so we can
	// quantify the offset before trusting Parallax SoC for energy math.
	// Guarded by mu.
	lastVehStateSoC map[string]float64

	// Live recording stores (all optional — nil stores disable that
	// particular writer). Samples captures every merged state update
	// as a row in vehicle_state. Drives and charges capture derived
	// sessions on gear/chargerState transitions.
	samplesStore *samples.Store
	drivesStore  *drives.Store
	chargesStore *charges.Store

	// elevation answers (lat, lon) -> meters lookups against an
	// in-memory tile cache backed by Mapzen Terrarium PNGs. Optional;
	// when nil the recorder writes NULL for altitude_m on every
	// sample. Cache misses are non-blocking (the resolver kicks off
	// async tile fetches) so this never slows the WS hot path.
	elevation ElevationLookup

	// routeFiller is invoked when the WS feed drops GPS fixes for
	// long enough that a straight line between the surrounding fixes
	// would visibly shortcut the actual route. Optional; when nil the
	// recorder falls back to the straight-line behavior.
	routeFiller RouteFiller

	// Per-vehicle in-flight session accumulators, keyed by vehicleID.
	// Access guarded by sessMu. Separate from mu so recorder work
	// doesn't serialize behind cache reads.
	//
	// rehydrated tracks which vehicles we've already attempted to
	// rehydrate from liveStateStore in this process. Looked up before
	// the lazy init at recordFrame so a single Redis miss after pod
	// boot doesn't keep retrying on every WS frame for the same
	// vehicle. Cleared on Unsubscribe so a re-acquired lease tries
	// again.
	sessMu     sync.Mutex
	sessions   map[string]*liveSessions
	rehydrated map[string]bool

	// liveStateStore persists liveSessions across pod restarts and
	// lease handoffs. Set via SetLiveStateStore at boot; nil disables
	// the rehydrate path (in-memory only). Best-effort: storage
	// failures degrade to fragmentation but never break the recorder.
	liveStateStore LiveStateStore

	// driveCloseHook is invoked asynchronously after every D→P
	// transition with a stable copy of the just-persisted drive row.
	// Used for post-close enrichment (weather fetch, etc.) that
	// should run as the drive is recorded rather than waiting for an
	// SPA backfill click. Set via SetDriveCloseHook; nil disables.
	driveCloseHook DriveCloseHook
	// hooksFired records which sessions' close hooks have fired
	// (hookOnce). Guarded by mu.
	hooksFired map[string]time.Time

	// chargeCloseHook mirrors driveCloseHook for charge sessions:
	// invoked asynchronously after charge close with a copy of the
	// just-persisted row so a push notification or post-close
	// enrichment can run without blocking the recorder. nil disables.
	chargeCloseHook ChargeCloseHook

	// Latest LiveSession payload per vehicle, refreshed by
	// chargingSessionPoller. Used by the recorder to enrich charge
	// rows with TotalChargedEnergyKWh / RangeAddedKm. Guarded by mu
	// alongside the state cache.
	lastSession map[string]*LiveSession

	// Per-vehicle bond between the WS-derived lastSession cache and
	// the recorder's currently-open liveCharge. Set to s.charge.id
	// when the recorder opens a new charge; cleared (empty string)
	// when it closes one. applyLiveSession stamps incoming pushes
	// with the bond active at the time of the push, and
	// upsertLiveCharge refuses to consume a cached LiveSession whose
	// stamp doesn't match the current charge -- closing the
	// "stale Parallax replay between sessions leaks energy into the
	// next session" hole that produced the v0.17.x phantom-charge
	// incident. Guarded by mu alongside lastSession.
	chargeBond     map[string]string
	lastSessionFor map[string]string

	// Per-vehicle metadata (model/trim/pack/image), fetched once at
	// startup via RefreshVehicleInfo. Consulted by the recorder to
	// pick an accurate pack size for the SoC-delta energy fallback.
	// Guarded by mu alongside the rest of the cache.
	vehicleInfo map[string]*Vehicle
	// Charging schedule reads (charging_schedule.go): a nudge channel per
	// vehicle, the last read's time and position, and the fetch (nil =
	// client.ChargingSchedules; tests set it). Guarded by mu.
	scheduleNudge  map[string]chan struct{}
	scheduleReads  map[string]scheduleRead
	// scheduleUnavailableUntil backs a vehicle's schedule reads off for a
	// week after Rivian said the account can't read them. Guarded by mu.
	scheduleUnavailableUntil map[string]time.Time
	fetchSchedules func(ctx context.Context, vehicleID string) ([]ChargingSchedule, error)
	// vehicleInfoHook receives each RefreshVehicleInfo result (trim, image,
	// pack guess) so it can be persisted. Set before Start; called outside
	// the monitor lock and must not block.
	vehicleInfoHook func([]Vehicle)

	// batteryCapacityHook fires from observeBatteryCapacity when the
	// vehicle-reported usable pack kWh changes. Wired by the
	// MonitorRegistry to a closure that UPDATEs vehicles.pack_kwh
	// so a process restart doesn't lose the live observation. nil
	// when no persistence is wired (legacy paths, tests).
	batteryCapacityHook func(vehicleID string, kwh float64)

	// startupStagger spaces out the first WS connect attempt of each
	// run() goroutine so a coordinator-driven mass-acquire (or a
	// pod restart with N existing leases) doesn't fire N parallel
	// SubscribeVehicleState calls into Rivian's gateway in the same
	// millisecond. Each run() takes one slot before its first
	// network call; serialized at staggerInterval (default 50ms).
	// The slot clock is a *Stagger so the registry can share one across every
	// user's monitor: a rollout that starts hundreds of single-vehicle users must
	// space the whole fleet's first connects, not each user's own.
	stagger *Stagger

	// priceLookup returns the user's configured home $/kWh rate
	// and currency at the time of call. Consulted by the recorder
	// when persisting a charge whose Rivian-reported price is absent
	// (every home AC / L2 session). Nil means "don't snapshot a
	// cost" — the charge row lands with Cost=0 and the read-path
	// decorator computes an estimate instead.
	priceLookup PriceLookup
	// dcfcPrice prices a DC fast-charging session Rivian didn't bill,
	// from the network of the station at its location. Nil leaves
	// such sessions on the home rate (the pre-0040 behaviour).
	dcfcPrice DCFCPriceLookup
	// dcfcDetect, when set and on for the user, replaces dcfcPrice with
	// network detection plus a verify state (dcfc_auto_network flag).
	dcfcDetect DCFCDetector
	// dcfcEstimate prices a DC fast charge the user has no rate for at
	// a typical public price, live sessions only (see DCFCEstimator).
	dcfcEstimate DCFCEstimator
	// homeLookup returns the user's saved home location. The recorder
	// uses it to leave AC charging away from home unpriced instead of
	// stamping the home rate on it. nil (or ok=false) = home unknown,
	// in which case slow charging keeps the home rate.
	homeLookup HomeLookup

	// sleepSafe reports whether a vehicle's non-essential
	// subscriptions should be parked while it sleeps, and
	// sleepObserver hears about every completed sleep. Both drive the
	// sleep gate (sleep_gate.go); nil disables each. Guarded by mu.
	sleepSafe     func(vehicleID string) bool
	sleepObserver SleepObserver

	// wakeSafety returns a vehicle's wake-safety switches (D2); nil is
	// all off. holdingVS marks vehicles whose vehicleState resubscribe
	// is being held (the watchdog ignores them). restFn is the hold's
	// REST check and periodicRefresh's poll, nil meaning
	// client.State (tests replace it).
	wakeSafety func(vehicleID string) WakeSafety
	holdingVS  map[string]bool
	restFn     func(ctx context.Context, vehicleID string) (*State, error)
	// restSkips counts rest_quiet skips per vehicle since its last REST
	// call. Guarded by mu.
	restSkips map[string]int
	// rest_quiet guardrail state (wake_safety.go), guarded by mu:
	// lastVSFrame is when a vehicleState WS frame last arrived;
	// restQuietSince when the current REST-quiet stretch began (reset
	// on wake and by a safety poll); lastSafetyPoll the last safety
	// poll.
	lastVSFrame    map[string]time.Time
	restQuietSince map[string]time.Time
	lastSafetyPoll map[string]time.Time

	// source, when set, replaces the live Rivian subscription: run()
	// feeds its frames straight to the recorder (see NewSourceMonitor).
	// client is nil on such monitors.
	source FrameSource
	// quietHooks is set while a source is replaying already-elapsed
	// frames, so close hooks (charge push notifications, drive weather
	// fetches) don't fire for sessions that ended in the past.
	quietHooks atomic.Bool
	// catchup buffers recorder writes while a source monitor replays
	// already-elapsed frames (replay_catchup.go). nil on live monitors.
	catchup *catchupWriter
}

// DCFCPriceLookup returns the user's $/kWh for a DC fast-charging
// session at (lat, lon), the currency, and the matched network's
// display name ("" when no station matched and a fallback rate was
// used). ok=false means no estimate is available.
type DCFCPriceLookup func(lat, lon float64) (ratePerKWh float64, currency, network string, ok bool)

// DCFCVerdict is a DC fast charge network detection, priced: the
// network, its rate for this user, and the verify state the charge row
// carries (contract dcfc-auto-network v1). Priced=false means nothing
// matched: the charge stays unpriced and needs the user.
type DCFCVerdict struct {
	Priced     bool
	Rate       float64
	Currency   string
	Network    string
	State      string
	Confidence float64
	Evidence   json.RawMessage
	// Slug is the matched network's slug ("" when nothing matched); the
	// recorder hands it to the DCFCEstimator when the user has no rate.
	Slug string
}

// DCFCEstimate is a fast-charging session's price taken from a typical
// public price (cost_source "observed_estimate"): never the user's own
// rate, which always wins, and replaced by Rivian's bill when it arrives.
type DCFCEstimate struct {
	Rate     float64
	Currency string
	Network  string
	// Month and URL name the report the figure came from ("2026-10",
	// the dcfctracker.com page).
	Month, URL string
}

// DCFCEstimator returns a typical-price estimate for a fast charge that
// started at start, when the user has no rate for it. slug is the
// detected network's, or "" to find the network at (lat, lon). ok=false
// leaves the session unpriced. Wired for the live recorder only; `rivolt
// reprice` never sets one.
type DCFCEstimator func(slug string, lat, lon float64, start time.Time) (DCFCEstimate, bool)

// DCFCDetector detects and prices the network of a DC fast charge Rivian
// didn't bill, at the charge's position (where it opened). on=false
// means auto-detection is off for this user: the recorder falls back to
// the DCFCPriceLookup. rivianCharger is Rivian's isRivianCharger for
// this charge's own session; peakKW is the session's peak so far.
type DCFCDetector func(lat, lon float64, rivianCharger bool, peakKW float64) (v DCFCVerdict, on bool)

// HomeLookup returns the user's saved home location; ok=false when the
// user hasn't set one.
type HomeLookup func() (lat, lon float64, ok bool)

// PriceLookup returns the current home electricity rate and its
// ISO-4217 currency code. Rate of 0 means "not configured"; callers
// should leave the persisted cost at zero in that case so it doesn't
// show up as a misleading $0.00 in history.
type PriceLookup func() (ratePerKWh float64, currency string)

// ElevationLookup is the minimal contract the recorder needs from
// an elevation resolver. Returns the altitude in meters, or ok=false
// when the tile is not (yet) cached -- the recorder writes NULL in
// that case rather than blocking on a network fetch. Implemented by
// *elevation.Resolver; abstracted here so the rivian package stays
// dep-free at the type-graph level and tests can stub it trivially.
type ElevationLookup interface {
	Lookup(lat, lon float64) (float64, bool)
}

// RouteFiller fills GPS-lag gaps in a live drive with a routing-engine
// shape between the last good fix and the next one. Returned slice
// includes both endpoints. Implemented by *maps.Valhalla; abstracted
// so the rivian package doesn't depend on internal/maps.
type RouteFiller interface {
	RouteShape(ctx context.Context, from, to [2]float64) ([][2]float64, error)
}

// NewStateMonitor wraps a live client. Pass a logger (usually from
// main.go's structured logger). nil is allowed; events will be
// discarded.
func NewStateMonitor(client *LiveClient, logger *slog.Logger) *StateMonitor {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	// RIVOLT_PARALLAX is the single operator switch for the whole Parallax
	// path — GPS backbone, authoritative drive dynamics, and the capture
	// topics — now that all three are proven and run everywhere. The old
	// granular RIVOLT_PARALLAX_{GPS,DRIVE_DYNAMICS,CAPTURE} vars are still
	// honored as a fallback so a mid-rollout config keeps working, but
	// RIVOLT_PARALLAX is the one flag going forward. (The internal fields
	// stay split for now — a no-risk config collapse; fully merging them
	// waits until the replay harness can validate the recorder offline.)
	parallaxAll := parseBoolEnv(os.Getenv("RIVOLT_PARALLAX"))
	return &StateMonitor{
		client:          client,
		logger:          logger,
		parallaxGPS:           parallaxAll || parseBoolEnv(os.Getenv("RIVOLT_PARALLAX_GPS")),
		parallaxDriveDynamics: parallaxAll || parseBoolEnv(os.Getenv("RIVOLT_PARALLAX_DRIVE_DYNAMICS")),
		parallaxCapture:       parallaxAll || parseBoolEnv(os.Getenv("RIVOLT_PARALLAX_CAPTURE")),
		lastVehStateGPS: make(map[string]time.Time),
		lastVehStateSoC: make(map[string]float64),
		lastParallaxAt:      make(map[string]time.Time),
		parallaxSleepFrames: make(map[string]int),
		lastTopicTs:         make(map[string]int64),
		lastPxGear:      make(map[string]string),
		lastPxGearAt:    make(map[string]time.Time),
		lastPxPower:     make(map[string]string),
		vehChargerStatus: make(map[string]bool),
		vehOtaVersion:    make(map[string]bool),
		cache:           make(map[string]*State),
		stamp:           make(map[string]time.Time),
		lastSampleAt:          make(map[string]time.Time),
		parkedDedupe:          parkedDedupeEnabled(),
		wsSeen:          make(map[string]bool),
		active:          make(map[string]context.CancelFunc),
		activeCtx:       make(map[string]context.Context),
		subCancel:       make(map[string]context.CancelCauseFunc),
		shortSubEnds:    make(map[string][]time.Time),
		lastFlapWarnAt:  make(map[string]time.Time),
		sessions:        make(map[string]*liveSessions),
		rehydrated:      make(map[string]bool),
		lastSession:     make(map[string]*LiveSession),
		chargeBond:      make(map[string]string),
		lastSessionFor:  make(map[string]string),
		vehicleInfo:     make(map[string]*Vehicle),
		// 50ms between WS subscribe attempts. With a 60-vehicle pod
		// that's a 3-second cold-start spread, well below Rivian's
		// observed rate-limit window.
		stagger: NewStagger(defaultStaggerInterval),
	}
}

// NewSourceMonitor returns a monitor whose vehicles are fed by src
// instead of a Rivian subscription — the replay/demo path. It has no
// client, so nothing it does can reach Rivian. vehicles seeds the
// metadata cache that RefreshVehicleInfo would otherwise fetch.
func NewSourceMonitor(src FrameSource, vehicles []Vehicle, logger *slog.Logger) *StateMonitor {
	m := NewStateMonitor(nil, logger)
	// Recorded frames are already the merged output of whichever feeds
	// produced them; the Parallax field-authority rules only apply to
	// live subscriptions.
	m.parallaxGPS, m.parallaxDriveDynamics, m.parallaxCapture = false, false, false
	m.source = src
	m.catchup = newCatchupWriter(m)
	for i := range vehicles {
		v := vehicles[i]
		m.vehicleInfo[v.ID] = &v
	}
	return m
}

// runSource is run() for source-fed monitors: frames are full
// snapshots, so each one becomes the cached state as-is (no delta
// merge) and drives the recorder exactly as a live push would.
func (m *StateMonitor) runSource(ctx context.Context, vehicleID string) {
	defer m.releaseActive(ctx, vehicleID)
	pubCtx, cancelPub := context.WithCancel(ctx)
	defer cancelPub()
	go m.liveStatePublisher(pubCtx, vehicleID)

	// Whatever catch-up buffered is persisted when the stream ends
	// (lease loss, shutdown), on a detached context.
	defer m.flushCatchup(vehicleID)

	err := m.source.Stream(ctx, vehicleID, func(st State, catchingUp bool) {
		if !catchingUp && m.quietHooks.Load() {
			// Caught up: persist the backlog before the first live
			// frame writes through, so writes stay in order.
			m.flushCatchup(vehicleID)
		}
		m.quietHooks.Store(catchingUp)
		cur := st
		cur.VehicleID = vehicleID
		sess := cur.ReplaySession
		cur.ReplaySession = nil
		m.mu.Lock()
		prev := m.cache[vehicleID]
		m.putCacheLocked(vehicleID, &cur)
		m.stamp[vehicleID] = time.Now()
		m.noteVehicleStateFrameLocked(vehicleID)
		m.mu.Unlock()
		m.record(ctx, vehicleID, prev, &cur)
		m.applyReplaySession(ctx, vehicleID, &cur, sess)
		if catchingUp && m.catchup.pendingSamples(vehicleID) >= catchupFlushSamples && m.catchup.flushDue(vehicleID) {
			m.flushCatchup(vehicleID)
		}
	})
	switch {
	case errors.Is(err, ErrVehicleNotFound):
		// A real vehicle owned by the same user: the source doesn't
		// serve it, and must not pretend to.
		m.logger.Debug("frame source: vehicle not served", "vehicle", vehicleID)
	case err != nil && ctx.Err() == nil:
		m.logger.Warn("frame source ended", "vehicle", vehicleID, "err", err.Error())
	}
}

// applyReplaySession applies a replayed frame's live-session snapshot
// (State.ReplaySession) after the frame is recorded, as the live
// charging-session feed would alongside it, so the open charge is
// bonded to it and closes with its billed price.
func (m *StateMonitor) applyReplaySession(ctx context.Context, vehicleID string, cur *State, sess *LiveSession) {
	if sess == nil {
		return
	}
	s := *sess
	s.VehicleID, s.At = vehicleID, cur.At
	if s.SoCPct == 0 {
		s.SoCPct = cur.BatteryLevelPct
	}
	m.applyLiveSession(ctx, vehicleID, &s)
}

// parseBoolEnv treats "1", "true", "yes", "on" (any case) as true; all
// else (including empty) is false. Central so feature flags read the
// same way everywhere.
func parseBoolEnv(v string) bool { return ParseBoolEnv(v) }

// ParseBoolEnv is parseBoolEnv for other packages' env flags (main's
// live-client guard), so every switch reads truthiness the same way.
func ParseBoolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// parallaxGPSStallThreshold is how long vehicleState can go without a
// GPS fix before the Parallax gnss subscriber starts filling points.
// vehicleState runs ~3s while driving, so 30s is unambiguously a stall
// (10+ missed frames), well clear of normal jitter. Parallax's own ~60s
// cadence caps how fast the fill can run once armed.
const parallaxGPSStallThreshold = 30 * time.Second

// parallaxStaleThreshold is how long the Parallax stream may go silent
// while the car is actively driving (powerState "go") before the liveness
// watch flags it. Parallax topics are event-driven, so silence while
// parked is normal — but while driving, gnss alone should push ~every 60s,
// so a multi-minute gap means the feed has silently died. Deliberately
// only meaningful during "go" to avoid false alarms on a parked-but-awake
// car whose topics simply have nothing to report.
const parallaxStaleThreshold = 3 * time.Minute

// lastParallaxGear returns the most recent mapped Parallax gear for the
// vehicle and when it arrived (zero values when none seen). Used by the
// recorder's drive-close debounce.
func (m *StateMonitor) lastParallaxGear(vehicleID string) (string, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastPxGear[vehicleID], m.lastPxGearAt[vehicleID]
}

// noteParallaxFrame records that some Parallax topic just delivered a
// frame. Called from every Parallax subscriber callback so
// parallaxLivenessWatch can distinguish a live-but-quiet feed from a dead
// one. Cheap: a single guarded map write.
//
// It reports whether the frame is FRESH: its event ts_ms is positive and
// either the first seen for this vehicle+topic or strictly newer than the
// last one. A frame with the same ts as before is a cloud replay of the
// last snapshot (every resubscribe re-sends it with its original ts_ms),
// not the vehicle emitting; callers that apply the value to live state
// must skip replays (see applyParallaxGear).
func (m *StateMonitor) noteParallaxFrame(vehicleID, topic string, tsMs int64) (fresh bool) {
	m.mu.Lock()
	m.lastParallaxAt[vehicleID] = time.Now()
	// Sleep hygiene: count only FRESH frames (event ts advancing past the
	// last seen for this topic) while asleep. The Rivian cloud replays the
	// last-known snapshot of every subscribed topic on a keepalive cadence
	// with a frozen ts_ms; those replays are not the vehicle emitting, so
	// they must not read as wake-keeping. tsMs<=0 (source without a usable
	// event time) is skipped — a real wake still surfaces via gnss /
	// drive-dynamics, which carry ts_ms.
	if tsMs > 0 {
		key := vehicleID + "|" + topic
		last, seen := m.lastTopicTs[key]
		if !seen || tsMs > last {
			fresh = true
			m.lastTopicTs[key] = tsMs
			// Only an advance past an established baseline is a new event;
			// the first sighting just sets the baseline.
			if seen {
				if st := m.cache[vehicleID]; st != nil &&
					strings.EqualFold(strings.TrimSpace(st.PowerState), "sleep") {
					m.parallaxSleepFrames[vehicleID]++
				}
			}
		}
	}
	m.mu.Unlock()
	return fresh
}

// parallaxGearMaxAge is how old a drive-dynamics frame's event time
// (gear, drive mode, power state) may be and still be applied. A real gear change reaches us within seconds;
// a replay after a resubscribe carries the timestamp of the last real
// change, which can be minutes old. This also covers the first frame
// seen after a restart (no baseline yet), which would otherwise count as
// fresh.
const parallaxGearMaxAge = 60 * time.Second

// parallaxLivenessWatch is the periodic Parallax observability tick. Two
// jobs:
//   - driving: warn when the stream goes silent while powerState=="go"
//     (the invisible-death case that must be detectable before
//     vehicleState can be dropped as the fallback — Phase 5).
//   - sleep: report the per-interval count of frames that arrived while
//     the car reported "sleep", proving Parallax doesn't keep a sleeping
//     car awake (0 = quiet).
// Observability only for now: it logs (no forced resubscribe yet, since
// vehicleState is still the authoritative fallback). The individual
// subscribers already reconnect on a hard error; this catches the harder
// case where the socket stays up but frames stop.
func (m *StateMonitor) parallaxLivenessWatch(ctx context.Context, vehicleID string) {
	t := time.NewTicker(parallaxStaleThreshold / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			last := m.lastParallaxAt[vehicleID]
			st := m.cache[vehicleID]
			sleepFrames := m.parallaxSleepFrames[vehicleID]
			m.parallaxSleepFrames[vehicleID] = 0
			m.mu.Unlock()
			ps := ""
			if st != nil {
				ps = strings.ToLower(strings.TrimSpace(st.PowerState))
			}
			// Only meaningful while driving: "go" is the state where
			// gnss must be streaming. Parked/asleep silence is expected.
			if ps == "go" && !last.IsZero() && now.Sub(last) > parallaxStaleThreshold {
				m.logger.Warn("parallax stream silent while driving",
					"vehicle", vehicleID,
					"since_last_frame", now.Sub(last).Round(time.Second).String())
			}
			// Sleep hygiene: sleepFrames counts only FRESH frames (ts_ms
			// advancing) that arrived while asleep — cloud keepalive replays
			// of a frozen snapshot are excluded. So a non-zero count means
			// the vehicle genuinely emitted new telemetry while "sleep",
			// which shouldn't happen and points at wake-keeping. 0 confirms
			// Parallax is only receiving passive replays and isn't poking
			// the car.
			if ps == "sleep" {
				if sleepFrames > 0 {
					m.logger.Info("parallax fresh frames while asleep",
						"vehicle", vehicleID, "fresh_frames", sleepFrames,
						"interval", (parallaxStaleThreshold / 2).String())
				} else {
					m.logger.Debug("parallax quiet while asleep (replays only)", "vehicle", vehicleID)
				}
			}
		}
	}
}

// noteVehStateGPS records that vehicleState just delivered a GPS fix, so
// the gnss subscriber can tell a live vehicleState feed from a stalled
// one. Only tracked when the master is on (no overhead in prod). Called
// with a raw vehicleState snapshot at each ingest point (WS push, REST
// seed, REST refresh); a frame without a fix (lat==0) doesn't count.
func (m *StateMonitor) noteVehStateGPS(vehicleID string, st *State) {
	if st == nil {
		return
	}
	// GPS liveness (gnss stall detection) and SoC reconciliation both key
	// off the raw vehicleState frame here, before Parallax may overwrite
	// the cache. SoC is stashed whenever Parallax SoC is authoritative so
	// the battery_state subscriber can log the delta.
	gps := st.Latitude != 0 && m.parallaxGPS
	soc := m.parallaxDriveDynamics && st.BatteryLevelPct > 0
	if !gps && !soc {
		return
	}
	m.mu.Lock()
	if gps {
		m.lastVehStateGPS[vehicleID] = time.Now()
	}
	if soc {
		m.lastVehStateSoC[vehicleID] = st.BatteryLevelPct
	}
	m.mu.Unlock()
}

// SetStores wires the recording stores. All three are optional — pass
// nil to disable that particular writer. Safe to call before Start;
// racy if called after subscriptions are running.
func (m *StateMonitor) SetStores(samplesStore *samples.Store, drivesStore *drives.Store, chargesStore *charges.Store) {
	m.samplesStore = samplesStore
	m.drivesStore = drivesStore
	m.chargesStore = chargesStore
}

// SetLiveStateStore wires the cross-restart accumulator persistence
// store. nil disables persistence and rehydration; the recorder runs
// purely from in-memory state (the pre-Redis behaviour). Safe to call
// before Start; racy if called after subscriptions are running.
func (m *StateMonitor) SetLiveStateStore(s LiveStateStore) {
	m.liveStateStore = s
}

// DriveCloseHook is invoked asynchronously after a D→P transition
// finishes upserting the drive row. ctx is bounded; the hook should
// honour it. Implementations must be safe for concurrent calls
// across vehicles.
type DriveCloseHook func(ctx context.Context, drv drives.Drive)

// SetDriveCloseHook wires a post-close enrichment callback (e.g.
// weather fetch). nil disables. Safe to call before Start; racy if
// called after subscriptions are running.
func (m *StateMonitor) SetDriveCloseHook(h DriveCloseHook) {
	m.driveCloseHook = h
}

// ChargeCloseHook is invoked asynchronously after a charge session
// closes (terminal charger state, plug pulled, or stale-session
// reaper). ctx is bounded; implementations must be safe for
// concurrent calls across vehicles.
type ChargeCloseHook func(ctx context.Context, c charges.Charge)

// SetChargeCloseHook wires a post-close callback for charge
// sessions. nil disables. Safe to call before Start.
func (m *StateMonitor) SetChargeCloseHook(h ChargeCloseHook) {
	m.chargeCloseHook = h
}

// SetElevationLookup wires an optional elevation resolver. nil
// disables altitude annotation; samples will be written with NULL
// altitude_m. Safe to call before Start; racy after.
func (m *StateMonitor) SetElevationLookup(e ElevationLookup) {
	m.elevation = e
}

// SetRouteFiller wires the GPS-gap fill backend. nil disables gap
// filling; long lags will leave a straight-line shortcut in the
// drive polyline (the pre-Valhalla behavior). Safe to call before
// Start; racy after.
func (m *StateMonitor) SetRouteFiller(r RouteFiller) {
	m.routeFiller = r
}

// SetPriceLookup wires a callback the recorder uses to stamp each
// closed charge with the current home $/kWh rate. Safe to call at
// any time; a nil value disables cost snapshotting.
func (m *StateMonitor) SetPriceLookup(fn PriceLookup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.priceLookup = fn
}

// SetHomeLookup wires the home-location lookup (see homeLookup).
func (m *StateMonitor) SetHomeLookup(fn HomeLookup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.homeLookup = fn
}

// SetDCFCDetector wires fast-charge network detection (see dcfcDetect).
func (m *StateMonitor) SetDCFCDetector(fn DCFCDetector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dcfcDetect = fn
}

// SetDCFCEstimator wires the live typical-price fallback (see dcfcEstimate).
func (m *StateMonitor) SetDCFCEstimator(fn DCFCEstimator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dcfcEstimate = fn
}

// SetDCFCPriceLookup wires the fast-charge pricer (see dcfcPrice).
func (m *StateMonitor) SetDCFCPriceLookup(fn DCFCPriceLookup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dcfcPrice = fn
}

// Start binds the monitor to a parent context. All subscriptions
// started via EnsureSubscribed use a child context derived from this
// parent; cancelling parent tears them all down.
func (m *StateMonitor) Start(ctx context.Context) {
	m.mu.Lock()
	m.parent = ctx
	m.mu.Unlock()

	// One-line record of the Parallax-GPS master so a pod's mode is
	// greppable without reading its env (distroless: no printenv,
	// non-root blocks /proc/<pid>/environ). The per-vehicle source is
	// logged separately ("gps source resolved") once SupportedFeatures
	// gating runs at subscribe time.
	m.logger.Info("state monitor starting", "parallax_gps_master", m.parallaxGPS)

	// Periodic janitor: close any live charge row left open from a
	// previous process death. The in-memory gear/charge mutex and
	// staleness guards in record() handle live cases, but neither
	// helps a row whose process died and never came back. Runs every
	// 10 min, marks anything still 'charging_*' with ended_at older
	// than 1h as 'abandoned'.
	// Source-fed monitors skip it: with no client the janitor can't
	// confirm a session over REST, and a replay closes its charges
	// through the normal lifecycle anyway.
	if m.chargesStore != nil && m.client != nil {
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

// sweepStaleCharges decides what to do with each open live charge row
// whose ended_at hasn't advanced for `staleAfter`. The first
// implementation just abandoned all of them, but L1/L2 home charges
// can stay open for >12h with the WS feed going dark for stretches
// at a time (deep-sleep cycles, LTE blips), so blanket abandonment
// closed real ongoing charges as collateral damage.
//
// Per-row policy now:
//  1. List the candidates (read-only).
//  2. REST-poll the vehicle gateway. If the gateway also says
//     chargerState is in a charging_* non-terminal state, refresh
//     the row's ended_at = NOW() so the next sweep gives it another
//     full window.
//  3. Otherwise — REST agrees charge is over, or the call failed —
//     close the row as 'abandoned'. Failing the REST and abandoning
//     is fine: if the WS *and* the REST are both silent for >1h on
//     a session, the row is genuinely orphaned.
func (m *StateMonitor) sweepStaleCharges(ctx context.Context, staleAfter time.Duration) {
	if m.chargesStore == nil {
		return
	}
	listCtx, listCancel := context.WithTimeout(ctx, 5*time.Second)
	candidates, err := m.chargesStore.ListStaleOpenLive(listCtx, time.Now().Add(-staleAfter))
	listCancel()
	if err != nil {
		m.noteWriteFailure("stale_charge", "", "", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	var refreshed, abandoned int
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		stillCharging, finalState := m.confirmStillCharging(ctx, c.RivianVehicleID)
		opCtx, opCancel := context.WithTimeout(ctx, 5*time.Second)
		if stillCharging {
			if _, err := m.chargesStore.RefreshOpenLive(opCtx, c.ExternalID, finalState); err != nil {
				m.noteWriteFailure("stale_charge", c.RivianVehicleID, c.ExternalID, err)
			} else {
				refreshed++
			}
		} else {
			if _, err := m.chargesStore.AbandonOpenLive(opCtx, c.ExternalID); err != nil {
				m.noteWriteFailure("stale_charge", c.RivianVehicleID, c.ExternalID, err)
			} else {
				abandoned++
			}
		}
		opCancel()
	}
	if refreshed > 0 || abandoned > 0 {
		m.logger.Info("stale charge janitor",
			"abandoned", abandoned, "refreshed", refreshed, "candidates", len(candidates))
	}
}

// confirmStillCharging asks the Rivian REST gateway whether the
// vehicle is still in a charging_* non-terminal state right now.
// Returns (true, currentChargerState) only on a clean response that
// agrees the session is live. On any error or ambiguity we return
// false so the caller falls back to the safe close path.
//
// Cache-first to protect the sleep budget: REST GetVehicleState is
// known to wake a deep-sleeping car on some firmwares (see
// adaptiveRefreshInterval), and a charging car typically goes to
// `powerState=sleep` once the BMS is happy with the SoC ramp. The
// WS subscription is the authoritative source of chargerState
// transitions \u2014 if the cache says still charging, we trust it
// rather than poking the car every sweep. We only fall through to
// REST when the cache itself is missing or already shows a
// non-charging state (which contradicts the row being open).
func (m *StateMonitor) confirmStillCharging(ctx context.Context, rivianVehicleID string) (bool, string) {
	if m.client == nil || rivianVehicleID == "" {
		return false, ""
	}

	// Cache check first.
	m.mu.RLock()
	cached := m.cache[rivianVehicleID]
	m.mu.RUnlock()
	if cached != nil && isChargingCS(cached.ChargerState) {
		// WS-cached state agrees the car is still charging. Don't
		// REST-poll \u2014 that would risk waking a sleeping car
		// unnecessarily. Refresh on cached state alone.
		return true, cached.ChargerState
	}

	// Cache says NOT charging (terminal state) or has no entry. The
	// row being open contradicts the cache, so fall back to a REST
	// confirm. Worst case we wake the car once per stale row, but
	// only when the data is genuinely ambiguous.
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	skip, why := m.restQuiet(rivianVehicleID, "stale_charge_confirm")
	if skip {
		// Asleep means not charging: the open row is stale.
		return false, ""
	}
	st, err := m.restState(cctx, rivianVehicleID, why)
	if err != nil || st == nil {
		if err != nil && ctx.Err() == nil {
			m.logger.Debug("stale charge REST confirm failed",
				"vehicle", rivianVehicleID, "err", err.Error())
		}
		return false, ""
	}
	if !isChargingCS(st.ChargerState) {
		return false, st.ChargerState
	}
	return true, st.ChargerState
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
	if m.activeCtx != nil {
		m.activeCtx[vehicleID] = ctx
	}
	m.mu.Unlock()

	go m.run(ctx, vehicleID)
}

// Subscribed reports whether a subscription goroutine is running for
// vehicleID (EnsureSubscribed started it and it hasn't exited).
func (m *StateMonitor) Subscribed(vehicleID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.active[vehicleID]
	return ok
}

// Unsubscribe tears down the background subscription for vehicleID
// if one is running, and is a no-op otherwise. Used by the lease
// coordinator when this pod loses ownership of a vehicle to a peer.
//
// Cancellation is async — the run goroutine observes ctx.Done() the
// next time it loops around. Everything this pod knew about the
// vehicle is forgotten (forgetVehicle): the new owner is the source
// of state from here on, and /api/state on this pod follows its
// published snapshot.
func (m *StateMonitor) Unsubscribe(vehicleID string) {
	m.mu.Lock()
	cancel, ok := m.active[vehicleID]
	if ok {
		// Drop the entry now so a concurrent EnsureSubscribed for a
		// re-acquired lease (acquired → released → reacquired in
		// quick succession) doesn't see a stale cancel func.
		delete(m.active, vehicleID)
		delete(m.activeCtx, vehicleID)
	}
	m.mu.Unlock()
	// A lease hand-off: write any session the throttle left behind before the
	// peer takes the vehicle over.
	fctx, fcancel := flushContext()
	m.flushVehicle(fctx, vehicleID)
	fcancel()
	m.forgetVehicle(vehicleID)
	if ok && cancel != nil {
		cancel()
	}
}

// forgetVehicle drops this pod's per-vehicle state when it stops
// streaming the vehicle, so a later re-acquire starts from what the
// peer owner left behind rather than from this pod's last frame hours
// ago:
//   - the merged State cache: the REST seed merges *under* it, so a
//     stale gear, location or charger state would win over a fresh
//     seed until Rivian happened to push that field again;
//   - the live-session cache and its charge bond;
//   - the open drive/charge accumulators, but only with a shared
//     store: a re-acquire then rehydrates the peer's snapshot (the
//     authoritative open-drive state) instead of extending or closing
//     a session the peer already closed. Without a store there is no
//     peer and nothing better to rebuild from, so they stay;
//   - cold-start tracking (poll fast until the first WS frame) and the
//     sample-spacing cursor (the first frame is always recorded).
func (m *StateMonitor) forgetVehicle(vehicleID string) {
	m.mu.Lock()
	delete(m.cache, vehicleID)
	delete(m.stamp, vehicleID)
	delete(m.lastSession, vehicleID)
	delete(m.chargeBond, vehicleID)
	delete(m.lastSessionFor, vehicleID)
	delete(m.wsSeen, vehicleID)
	delete(m.wokeAt, vehicleID)
	m.mu.Unlock()
	m.sessMu.Lock()
	if m.liveStateStore != nil {
		delete(m.sessions, vehicleID)
	}
	delete(m.rehydrated, vehicleID)
	m.sessMu.Unlock()
	m.forgetSampleSlot(vehicleID)
}

// releaseActive is how a run goroutine gives up its active entry when
// it exits. It removes the entry only if it is still this goroutine's
// (a quick release → re-acquire may already have installed a
// successor, which an unconditional delete would orphan: Unsubscribe
// could then never cancel it). When the goroutine ends because it was
// cancelled and nothing replaced it, the vehicle is forgotten again:
// frames recorded during teardown, after Unsubscribe's forget, would
// otherwise leave a stale accumulator behind.
func (m *StateMonitor) releaseActive(ctx context.Context, vehicleID string) {
	m.mu.Lock()
	cur, tracked := m.activeCtx[vehicleID]
	switch {
	case tracked && cur == ctx:
		delete(m.active, vehicleID)
		delete(m.activeCtx, vehicleID)
	case !tracked:
		// Untracked (a monitor built without activeCtx): keep the old
		// behavior.
		delete(m.active, vehicleID)
	}
	_, successor := m.active[vehicleID]
	m.mu.Unlock()
	if ctx.Err() != nil && !successor {
		m.forgetVehicle(vehicleID)
	}
}

// run is the per-vehicle subscription goroutine. It blocks inside
// SubscribeVehicleState, which internally reconnects with backoff on
// transient errors, and only returns when ctx is cancelled or the
// server rejects the session token.
func (m *StateMonitor) run(ctx context.Context, vehicleID string) {
	// Stagger first contact so a thundering herd (post-restart with
	// N existing leases, or a coordinator that just acquired a batch)
	// doesn't fire N concurrent SubscribeVehicleState calls into
	// Rivian's gateway. The wait is bounded by ctx cancellation so
	// Unsubscribe still returns promptly during a rebalance.
	m.waitStaggerSlot(ctx)
	if ctx.Err() != nil {
		m.releaseActive(ctx, vehicleID)
		return
	}
	if m.source != nil {
		m.runSource(ctx, vehicleID)
		return
	}

	// Block until the client holds a usable session. Without this
	// gate every per-vehicle goroutine spins through Subscribe →
	// ErrNotAuthenticated → 1m backoff forever when the pod boots
	// before any user has logged in (or before secrets-store
	// hydration has populated the live client). The auth-ready
	// channel is closed by Login / LoginWithOTP / Restore on the
	// LiveClient and replaced on Logout, so Logout-then-Login does
	// the right thing.
	if !m.waitAuthReady(ctx, vehicleID) {
		return
	}

	m.logger.Info("rivian ws subscribe", "vehicle", vehicleID)

	// Decide whether to run the Parallax GPS subscriber for this
	// vehicle. Two-layer gate mirroring the app: the RIVOLT_PARALLAX_GPS
	// master must be on AND the vehicle must advertise Parallax
	// connectivity (VEHICLE_CONNECTIVITY_PARALLAX=AVAILABLE) via
	// SupportedFeatures. We gate on connectivity, not the full-migration
	// PX_STATE_ALL capability, because we cherry-pick topics rather than
	// replacing vehicleState wholesale. Errors leave the vehicle on
	// vehicleState alone (dense but stall-prone); when eligible, Parallax
	// gnss becomes the primary GPS backbone with vehicleState as the
	// detail filler between its ticks.
	eligibleParallaxGPS := false
	pxStatus := "master_off"
	if m.parallaxGPS {
		all, err := m.client.SupportedFeatures(ctx)
		if err != nil {
			pxStatus = "probe_error"
			if ctx.Err() == nil {
				m.logger.Warn("supported-features probe failed; staying on vehicleState GPS",
					"vehicle", vehicleID, "err", err.Error())
			}
		} else {
			feats := all[vehicleID]
			connectivity := feats[FeatureConnectivityParallax]
			eligibleParallaxGPS = connectivity == FeatureStatusAvailable
			pxStatus = feats[FeatureParallaxVehicleState]
			if pxStatus == "" {
				pxStatus = "absent"
			}
			names := make([]string, 0, len(feats))
			for n := range feats {
				names = append(names, n)
			}
			sort.Strings(names)
			m.logger.Info("supported features",
				"vehicle", vehicleID,
				"connectivity_parallax", connectivity,
				"px_state_all", pxStatus,
				"features", strings.Join(names, ","))
		}
	}
	source := "vehicle_state"
	if eligibleParallaxGPS {
		source = "parallax gnss primary + vehicle_state detail-fill"
	}
	m.logger.Info("gps source resolved", "vehicle", vehicleID, "gps_source", source,
		"connectivity_parallax", eligibleParallaxGPS, "px_state_all", pxStatus,
		"master", m.parallaxGPS)

	// Seed the cache from REST before the subscription starts
	// streaming. Rivian's subscription pushes deltas, so if we don't
	// establish a baseline the cache only ever contains whichever
	// handful of fields happened to change since connect — the rest
	// render as em-dashes in the UI. A REST GetVehicleState fills
	// odometer, gear, lat/lon, charger_state, etc. so mergeState has
	// something to overlay the deltas onto. Tire pressures (bar) and
	// other subscription-only fields stay zero here and get filled
	// in once the first push arrives.
	if st, err := m.restState(ctx, vehicleID, "seed"); err == nil && st != nil {
		m.noteVehStateGPS(vehicleID, st)
		m.mu.Lock()
		var merged *State
		prev := m.cache[vehicleID]
		if prev == nil {
			merged = st
		} else {
			// A push may have raced us here; fold REST under it.
			merged = mergeState(st, prev)
		}
		m.putCacheLocked(vehicleID, merged)
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
	// Publish this vehicle's merged live State to the shared store so a
	// peer replica that doesn't own the lease can serve /api/state with
	// all subscription-only fields (see RemoteLatest). No-op without a
	// shared store.
	go m.liveStatePublisher(refreshCtx, vehicleID)
	// Everything else that talks to Rivian besides the vehicleState
	// WebSocket below runs under the sleep gate, which parks it while a
	// sleep-safe vehicle is asleep and restarts it on wake (see
	// sleep_gate.go). For other vehicles the gate only observes.
	go m.sleepGate(refreshCtx, vehicleID, func(wctx context.Context) {
		go m.periodicRefresh(wctx, vehicleID)
		go m.chargingSessionMetadataFetcher(wctx, vehicleID)
		go m.chargingScheduleLoop(wctx, vehicleID)
		go m.chargingSessionSubscriber(wctx, vehicleID)
		go m.batteryStateSubscriber(wctx, vehicleID)
		if eligibleParallaxGPS {
			go m.dynamicsGNSSSubscriber(wctx, vehicleID)
			// Liveness watch (Phase-5 prerequisite): warn if the Parallax
			// stream goes silent while driving — the invisible-death case that
			// must be detectable before vehicleState can be dropped.
			go m.parallaxLivenessWatch(wctx, vehicleID)
			// Phase 2 drive-dynamics: shadow-log gear/drive_mode/odometer/
			// power.state vs vehicleState and apply them authoritatively
			// (gear opens drives, odometer bridges stalls, power.state /
			// drive_mode fill the cache).
			if m.parallaxDriveDynamics {
				go m.driveDynamicsSubscriber(wctx, vehicleID)
			}
		}
	})

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
		// Re-gate on auth between resubscribes. If the upstream
		// rejected our session mid-flight (Logout, server-side
		// expiry that produced an ErrNotAuthenticated return) we
		// must not spin tight on Subscribe; wait for the next
		// successful Login/Restore to close authReady.
		if !m.client.Authenticated() {
			if !m.waitAuthReady(ctx, vehicleID) {
				break
			}
		}
		// A restored-but-flagged session is authenticated (u-sess
		// present) yet doomed: Rivian won't feed the WS until the user
		// re-auths. Park here instead of spinning up zombie sockets.
		if !m.waitReauthClear(ctx, vehicleID) {
			break
		}
		// WithCancelCause so the watchdog can stamp the cancellation
		// with errStaleSubscription. Without that we can't
		// distinguish "Rivian gateway stopped pushing for 10m and we
		// killed the WS" (= the symptom we want to escalate on) from
		// "TCP RST / network error" (= retry quietly), since both
		// surface as `err = context canceled` once cancelSub fires.
		subCtx, cancelSub := context.WithCancelCause(ctx)
		// Publish the cancel handle so periodicRefresh can poke us
		// when REST detects the car just woke up (otherwise we'd
		// wait up to wsStaleThreshold for the watchdog). Cleared
		// below on return.
		m.setSubCancel(vehicleID, subCtx, cancelSub)
		// Reset stamp cursor so the watchdog measures this attempt,
		// not a stale value from a prior failed connection. Track
		// connect time + frame count so we can distinguish three
		// outcomes after the call returns:
		//   1. healthy session that lasted long enough to be
		//      considered "good" before going stale — reset backoff.
		//   2. zombie: subscription went silent (watchdog fired)
		//      OR received zero frames before the watchdog killed
		//      it. Both are signals that Rivian's gateway has
		//      stopped honoring the subscription, typically because
		//      the userSessionToken expired server-side.
		//   3. fast failure — apply backoff.
		m.mu.Lock()
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
		var frameCount int64
		connectAt := time.Now()
		go m.watchSubscription(subCtx, vehicleID, cancelSub)

		err := m.client.SubscribeVehicleStateHeld(subCtx, vehicleID, m.vehicleStateHold(vehicleID), func(st *State) {
			atomic.AddInt64(&frameCount, 1)
			m.applyVehicleStatePush(ctx, vehicleID, st)
		})
		cancelSub(nil)
		m.clearSubCancel(vehicleID, subCtx)
		if ctx.Err() != nil {
			break
		}
		sessionDur := time.Since(connectAt)
		frames := atomic.LoadInt64(&frameCount)
		// errors.Is so any wrapped form (context.Cause may chain)
		// still classifies. errStaleSubscription is the watchdog's
		// signature.
		watchdogKilled := errors.Is(context.Cause(subCtx), errStaleSubscription)
		// Resubscribe nudge from periodicRefresh \u2014 not a fault, not
		// a zombie, just a fast bounce so a freshly-woken car gets
		// a clean WS without waiting for the watchdog window.
		nudged := errors.Is(context.Cause(subCtx), errResubscribeRequested)

		// Flap detection: a burst of short sessions means a competing
		// subscriber to the same VIN keeps kicking us (see noteSubEnd).
		m.noteSubEnd(vehicleID, sessionDur, nudged)

		switch {
		case nudged:
			// Wake-up nudge from periodicRefresh. Reset backoff so
			// the next Subscribe call fires immediately.
			m.logger.Info("rivian ws resubscribe nudge",
				"vehicle", vehicleID,
				"session_dur", sessionDur.Round(time.Second).String(),
				"frames", frames)
			backoff = time.Second
		case watchdogKilled || (frames == 0 && sessionDur > wsStaleThreshold/2):
			// Zombie: Rivian's gateway stopped pushing telemetry —
			// either it never started (frames == 0, classic
			// stale-token-on-connect) or it went silent mid-session
			// long enough for the watchdog to kill the WS. Logged
			// loudly as a top-of-list dashboard signal, but we do
			// NOT auto-flip needs_reauth:
			//   1. Rivian exposes no public refreshTokens mutation
			//      (verified against jrgutier/rivian-python-client
			//      and home-assistant-rivian), so the only recovery
			//      is full re-login + OTP — disruptive enough that
			//      false positives are worse than false negatives.
			//   2. A genuinely sleeping car or weak-LTE pocket can
			//      produce 30+ min of legitimate silence on a still
			//      valid token; the heartbeat assumption is not a
			//      contract. Auto-tripping reauth on those would
			//      OTP-spam the user.
			// The user notices missing data and re-logs in manually
			// — annoying but recoverable; ops still gets the signal
			// via the ERROR log when investigating gaps.
			reason := "frames=0 from connect"
			if watchdogKilled {
				reason = "watchdog: no push for wsStaleThreshold"
			}
			m.logger.Error("rivian ws zombie subscription",
				"vehicle", vehicleID,
				"session_dur", sessionDur.Round(time.Second).String(),
				"frames", frames,
				"reason", reason,
				"hint", "if persistent, user re-login may be required")
		case err != nil:
			m.logger.Warn("rivian ws subscribe ended, retrying",
				"vehicle", vehicleID, "err", err.Error(),
				"frames", frames, "session_dur", sessionDur.Round(time.Second).String(),
				"backoff", backoff.String())
		default:
			m.logger.Info("rivian ws subscribe returned cleanly, resubscribing",
				"vehicle", vehicleID,
				"frames", frames, "session_dur", sessionDur.Round(time.Second).String(),
				"backoff", backoff.String())
		}

		// Long, healthy sessions that go stale are not faults — a
		// car that drove for 2h then went to sleep shouldn't make
		// us wait 64s before resubscribing. Reset the backoff
		// staircase whenever the call returned after wsStaleThreshold,
		// regardless of which "ended" branch we took, as long as we
		// actually saw frames during the run AND it wasn't a
		// watchdog kill (zombies don't deserve a fast retry).
		if !watchdogKilled && frames > 0 && sessionDur >= wsStaleThreshold {
			backoff = time.Second
		}

		// Jitter the actual sleep ±50% so concurrent vehicles whose
		// sessions died from the same upstream blip don't reconnect
		// in lockstep.
		select {
		case <-ctx.Done():
		case <-time.After(jitter(backoff)):
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
	m.releaseActive(ctx, vehicleID)
}

// wsStaleThreshold is how long we tolerate no WS pushes before
// assuming the socket has gone zombie and forcing a resubscribe.
// Rivian pushes at least powerState / chargerStatus heartbeats every
// few minutes on a live session; 10 min is comfortably above normal
// quiet periods for a sleeping car (which also gets periodic status
// frames) while still recovering quickly from a silent dropout.
const wsStaleThreshold = 10 * time.Minute

// Flap detection: a competing subscriber to the same vehicle (same VIN
// connected from another Rivolt instance / Rivian account) repeatedly
// kicks our WS, producing a burst of very short sessions. A healthy
// session lasts far longer — until the car sleeps and Rivian idles it.
const (
	flapSessionMax   = 3 * time.Minute  // shorter than this = a "kicked" session
	flapWindow       = 20 * time.Minute // sliding window
	flapCount        = 8                // short sessions in the window → flapping
	flapWarnCooldown = 20 * time.Minute // rate-limit the loud warning
)

// noteSubEnd records that a WS session for vehicleID ended after
// sessionDur and warns loudly when short sessions pile up — the
// signature of a competing subscriber to the same vehicle repeatedly
// kicking our feed (which surfaces days later as phantom drives and
// split-brain state if undetected). Intentional nudge resubscribes and
// normal long sessions don't count.
// Returns true when this call crossed the threshold and emitted the
// warning (also the assertion hook for tests).
func (m *StateMonitor) noteSubEnd(vehicleID string, sessionDur time.Duration, nudged bool) bool {
	if nudged || sessionDur >= flapSessionMax {
		return false
	}
	now := time.Now()
	m.mu.Lock()
	ends := append(m.shortSubEnds[vehicleID], now)
	cutoff := now.Add(-flapWindow)
	kept := ends[:0]
	for _, t := range ends {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.shortSubEnds[vehicleID] = kept
	n := len(kept)
	warn := n >= flapCount && now.Sub(m.lastFlapWarnAt[vehicleID]) >= flapWarnCooldown
	if warn {
		m.lastFlapWarnAt[vehicleID] = now
	}
	m.mu.Unlock()
	if warn {
		m.logger.Error("rivian ws feed flapping — likely a competing subscriber to this vehicle",
			"vehicle", vehicleID,
			"short_sessions", n,
			"window", flapWindow.String(),
			"hint", "another Rivolt instance / Rivian account is live-connected to the same VIN; only one live subscription per vehicle survives, so they kick each other and fragment recording")
	}
	return warn
}

// watchSubscription force-cancels the per-subscribe context if no
// push has landed for wsStaleThreshold. Exits when its context is
// cancelled by the subscribe loop (normal completion) or when it
// fires the cancel itself (zombie detected). Safe to call cancel()
// multiple times — context.CancelCauseFunc is idempotent (subsequent
// calls are no-ops, the first cause wins).
func (m *StateMonitor) watchSubscription(ctx context.Context, vehicleID string, cancel context.CancelCauseFunc) {
	t := time.NewTicker(wsStaleThreshold / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.RLock()
			last := m.stamp[vehicleID]
			holding := m.holdingVS[vehicleID]
			m.mu.RUnlock()
			// A wake-safety hold is silence on purpose, not a zombie.
			if holding {
				continue
			}
			if !last.IsZero() && now.Sub(last) > wsStaleThreshold {
				m.logger.Warn("rivian ws stale, forcing resubscribe",
					"vehicle", vehicleID,
					"since_last_push", now.Sub(last).Round(time.Second).String())
				cancel(errStaleSubscription)
				return
			}
		}
	}
}

// errStaleSubscription is the cancel cause stamped by
// watchSubscription so the resubscribe loop can tell a watchdog kill
// (= zombie) apart from a normal network error or ctx-driven
// shutdown. Never returned by SubscribeVehicleState directly; only
// observable via context.Cause(subCtx) after the call returns.
var errStaleSubscription = errors.New("rivian ws stale, watchdog cancelled subscription")

// errResubscribeRequested is the cancel cause stamped by the
// resubscribe nudge path (periodicRefresh \u2192 kickResubscribe). Treated
// as a clean reset by the supervise loop: no zombie warning, backoff
// snaps to 1s so the next SubscribeVehicleState fires immediately.
// Used when REST observes the car just woke up while the WS has been
// quiet \u2014 we don't want to wait up to wsStaleThreshold for the
// watchdog to discover what we already know.
var errResubscribeRequested = errors.New("rivian ws resubscribe requested by wakeup nudge")

// setSubCancel publishes the current vehicleState subscription's
// cancel handle (see kickResubscribe). A subscription already cancelled
// (lease lost or monitor stopped before it published) publishes
// nothing: it is about to return, and its handle would only replace a
// successor's, leaving kickResubscribe pointed at a dead context.
func (m *StateMonitor) setSubCancel(vehicleID string, ctx context.Context, cancel context.CancelCauseFunc) {
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subCtx == nil {
		m.subCtx = map[string]context.Context{}
	}
	m.subCancel[vehicleID] = cancel
	m.subCtx[vehicleID] = ctx
}

// clearSubCancel drops the subscription's cancel handle when it ends,
// only if it is still that subscription's: after a quick release →
// re-acquire the successor may already have published its own, which an
// unconditional delete removed (kickResubscribe then couldn't nudge it).
func (m *StateMonitor) clearSubCancel(vehicleID string, ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.subCtx[vehicleID]; ok && cur != ctx {
		return
	}
	delete(m.subCancel, vehicleID)
	delete(m.subCtx, vehicleID)
}

// kickResubscribe cancels the active SubscribeVehicleState context
// for vehicleID, if any, with errResubscribeRequested as the cause.
// No-op if there is no active subscription (e.g. the supervisor is
// in backoff between attempts). Caller must NOT hold m.mu.
func (m *StateMonitor) kickResubscribe(vehicleID, reason string) {
	m.mu.Lock()
	cancel := m.subCancel[vehicleID]
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	m.logger.Info("rivian ws kicking resubscribe",
		"vehicle", vehicleID, "reason", reason)
	cancel(errResubscribeRequested)
}

// adaptiveRefreshInterval picks a REST refresh cadence based on the
// cached power state. REST GetVehicleState is known to bump the
// vehicle out of deep sleep in some firmwares (home-assistant-rivian
// explicitly refuses to poll vehicleState for this reason), so we
// only poll aggressively when the car is demonstrably awake. The WS
// subscription keeps running at all power states; REST is only a
// backfill for fields Rivian's subscription doesn't replay.
//
//	asleep / idle-plugged: 30 min - minimal wake pressure, we have WS
//	standby / ready:       10 min - car is awake anyway
//	go / active charge:     2 min - driving or drawing power, want freshness
//
// Cold-start exception: until we've received our first WS frame for
// this vehicle, poll at the 2-min cadence regardless of cached
// PowerState. A pod that booted while a trip was already underway
// (or while the gateway's cached frame was stale) needs fast REST to
// detect the in-progress state — we'd otherwise sit on a stale
// "charging_complete" cache for 30 min and miss the start of a
// drive entirely (observed: 2026-05-01 home→Junction, 8.5 h gap).
func (m *StateMonitor) adaptiveRefreshInterval(vehicleID string) time.Duration {
	m.mu.RLock()
	st := m.cache[vehicleID]
	wsSeen := m.wsSeen[vehicleID]
	m.mu.RUnlock()
	if !wsSeen {
		return 2 * time.Minute
	}
	if st == nil {
		return 30 * time.Minute
	}
	switch strings.ToLower(st.PowerState) {
	case "go":
		return 2 * time.Minute
	case "ready", "standby":
		return 10 * time.Minute
	case "sleep", "":
		// Sleeping or unknown. Only fast-poll when the pack is actually
		// drawing power; charging_ready/_complete are connected-but-idle
		// and shouldn't pull the cadence down to 2 min.
		cs := strings.ToLower(strings.TrimSpace(st.ChargerState))
		if cs == "charging_active" || cs == "charging_connecting" {
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
		if !m.waitRefresh(ctx, vehicleID, m.adaptiveRefreshInterval(vehicleID)) {
			return
		}
		m.refreshOnce(ctx, vehicleID)
	}
}

// refreshOnce is one periodicRefresh tick: pull REST and fold it under
// the cache.
func (m *StateMonitor) refreshOnce(ctx context.Context, vehicleID string) {
	// Experiment A (wake_safety rest_quiet): no REST poll while the
	// car reports asleep. The vehicleState subscription stays up and
	// is how a wake is noticed; the next tick after it re-polls.
	skip, why := m.restQuiet(vehicleID, "periodic_refresh")
	if skip {
		return
	}
	st, err := m.restState(ctx, vehicleID, why)
	if err != nil {
		if ctx.Err() == nil {
			m.logger.Debug("rivian rest refresh failed", "vehicle", vehicleID, "err", err.Error())
		}
		return
	}
	if st == nil {
		return
	}
	m.noteVehStateGPS(vehicleID, st)
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
	m.putCacheLocked(vehicleID, merged)
	m.stamp[vehicleID] = time.Now()
	m.mu.Unlock()
	// Resubscribe nudge: REST just observed an interesting
	// transition (car woke up / plugged in / shifted into D /
	// SoC moved meaningfully). Kick the supervise loop instead
	// of waiting up to wsStaleThreshold for the watchdog to
	// notice. No-op when no Subscribe is currently active.
	if prev != nil && wakeWorthyTransition(prev, st) {
		m.kickResubscribe(vehicleID, "rest detected wakeup transition")
	}
	// Sample-only: REST snapshots can replay a stale frame for
	// hours when the car is in a cellular dead-zone (gateway
	// keeps serving its last known gear/lat/lon/speed). Letting
	// that drive lifecycle decisions fragmented a single 3-hour
	// drive into 40+ stub drives. Lifecycle is now WS-driven
	// only; REST stays a pure cache backfill.
	m.recordSampleOnly(ctx, vehicleID, prev, merged)
}

// wakeWorthyTransition returns true when the REST snapshot reveals
// state that the cached frame doesn't show, in a way that means the
// car is awake and likely emitting telemetry. We bias toward false
// negatives (over-trigger is just an extra reconnect, but waking a
// sleeping car for nothing is bad). Triggers on:
//   - powerState moving out of sleep/empty into anything else
//   - gear transitioning into a driving gear
//   - chargerState transitioning into an active charging state
//   - SoC delta > 1% (whatever happened, the BMS is awake)
func wakeWorthyTransition(prev, curr *State) bool {
	if prev == nil || curr == nil {
		return false
	}
	prevPS := strings.ToLower(strings.TrimSpace(prev.PowerState))
	currPS := strings.ToLower(strings.TrimSpace(curr.PowerState))
	wasAsleep := prevPS == "" || prevPS == "sleep"
	if wasAsleep && currPS != "" && currPS != "sleep" {
		return true
	}
	if !isDrivingGear(prev.Gear) && isDrivingGear(curr.Gear) {
		return true
	}
	prevCharging := isChargingCS(prev.ChargerState)
	currCharging := isChargingCS(curr.ChargerState)
	if !prevCharging && currCharging {
		return true
	}
	if d := curr.BatteryLevelPct - prev.BatteryLevelPct; d > 1 || d < -1 {
		return true
	}
	return false
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

// batteryStateSubscriber streams the Parallax
// energy.high_voltage.battery_state topic for the monitor's lifetime and
// folds the pack cell temperatures into the cached State, so the next
// recorded vehicleState frame carries them (mergeState keeps them across
// deltas). Unlike the charging subscriber this runs continuously, not
// just while charging — pack temperature is worth recording whenever the
// vehicle is awake. SubscribeBatteryState handles reconnect/backoff.
func (m *StateMonitor) batteryStateSubscriber(ctx context.Context, vehicleID string) {
	err := m.client.SubscribeBatteryState(ctx, vehicleID, func(bt *BatteryTemp) {
		// bt.Timestamp is a string of uncertain format; skip freshness
		// (tsMs 0) — a real wake surfaces via gnss / drive-dynamics.
		m.noteParallaxFrame(vehicleID, rvmBatteryState, 0)
		if bt == nil {
			return
		}
		if bt.SoCPct != 0 || bt.PackKWh != 0 {
			// Phase-3 reconciliation: log Parallax SoC next to the last
			// vehicleState SoC and their delta so the offset (≈1% observed)
			// can be quantified before Parallax SoC is trusted for the
			// drive/charge energy math it already feeds.
			m.mu.RLock()
			vehSoC := m.lastVehStateSoC[vehicleID]
			m.mu.RUnlock()
			var socDelta float64
			if vehSoC > 0 && bt.SoCPct > 0 {
				socDelta = bt.SoCPct - vehSoC
			}
			m.logger.Info("parallax battery_state charge",
				"vehicle", vehicleID, "px_soc", bt.SoCPct, "px_pack_kwh", bt.PackKWh,
				"vehicleState_soc", vehSoC, "soc_delta", socDelta)
		}
		m.mu.Lock()
		if st := m.cache[vehicleID]; st != nil {
			// Pack temp has been authoritative since the pack-temp work;
			// it stays ungated (no vehicleState equivalent).
			if bt.CellAvgC != 0 {
				st.PackTempAvgC = bt.CellAvgC
				st.PackTempMaxC = bt.CellMaxC
				st.PackTempMinC = bt.CellMinC
			}
			// SoC/capacity gate on the same drive-dynamics flag as the
			// other Parallax-authoritative fields, so they stay off in prod
			// (vehicleState) until the flag is flipped there. Range guards
			// also reject a mis-decoded (wrong wire type) value.
			if m.parallaxDriveDynamics {
				if bt.SoCPct > 0 && bt.SoCPct <= 100 {
					st.BatteryLevelPct = bt.SoCPct
				}
				if bt.PackKWh > 0 && bt.PackKWh < 300 {
					st.BatteryCapacityKWh = bt.PackKWh
				}
			}
		}
		m.mu.Unlock()
		// Persist the vehicle-reported capacity straight through to
		// vehicles.pack_kwh (observeBatteryCapacity fires the write-through
		// hook) rather than waiting for a recordFrame merge to carry it —
		// so PackKWhFor and the pack-health card's current-vs-documented
		// both see the live value. Called outside the lock (it takes m.mu).
		if m.parallaxDriveDynamics && bt.PackKWh > 0 && bt.PackKWh < 300 {
			m.observeBatteryCapacity(vehicleID, bt.PackKWh)
		}
	})
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		m.logger.Warn("battery_state subscription ended", "vehicle", vehicleID, "err", err.Error())
	}
}

// dynamicsGNSSSubscriber streams the Parallax dynamics.vehicle.gnss
// topic — the PRIMARY GPS backbone. Every frame applies to the cache and
// drives a recorder pass (except exact-duplicate parked replays).
// vehicleState's denser ~3s fixes fill detail between gnss ticks via the
// normal frame path, demoted to that filler role by the recorder's
// fix-timestamp monotonicity guard (they stall for minutes and replay
// stale fixes; observed 2026-07-14: a 5-min full blackout mid-drive).
// Started only for vehicles past the eligibility gate
// (master + VEHICLE_CONNECTIVITY_PARALLAX).
//
// Runs full lifecycle (not sample-only) because these frames are live,
// so a point still updates the drive's speed aggregates and end
// state. Gear/SoC/charger carry over from the cached vehicleState frame,
// so a gnss frame never opens or closes a session on its own; only a
// real gear change does. Deliberately does NOT
// advance m.stamp: that cursor gates the vehicleState WS watchdog, and
// Parallax keeping it warm would mask a dead vehicleState feed.
func (m *StateMonitor) dynamicsGNSSSubscriber(ctx context.Context, vehicleID string) {
	err := m.client.SubscribeDynamicsGNSS(ctx, vehicleID, func(g *DynamicsGNSS) {
		var gnssTs int64
		if g != nil {
			gnssTs = g.TimestampMs
		}
		m.noteParallaxFrame(vehicleID, "dynamics.vehicle.gnss", gnssTs)
		if g == nil {
			return
		}
		m.mu.Lock()
		prev := m.cache[vehicleID]
		if prev == nil {
			// No base frame yet — wait for the vehicleState seed so the
			// recorded row carries gear/SoC/odometer, not a bare fix.
			m.mu.Unlock()
			return
		}
		// Parallax gnss is the primary GPS backbone: every frame applies
		// unconditionally (steady ~60s cadence, no multi-minute stalls —
		// unlike vehicleState, whose dense ~3s fixes stall for minutes
		// and die in connectivity blackouts). vehicleState's fixes still
		// flow through the normal frame path and fill fine-grained
		// detail *between* gnss ticks; the recorder's fix-timestamp
		// monotonicity guard (liveDrive.lastFixTS) demotes them to that
		// filler role by rejecting any fix older than the newest one
		// already applied, so a stale vehicleState replay can't drag
		// the path backwards.
		//
		// Skip only exact-duplicate parked frames (same fix replayed) to
		// avoid pointless recorder passes while stationary.
		if prev.Latitude == g.Latitude && prev.Longitude == g.Longitude && g.SpeedMS <= 0 {
			m.mu.Unlock()
			return
		}
		lastVeh := m.lastVehStateGPS[vehicleID]
		next := *prev
		next.At = time.Now()
		next.Latitude = g.Latitude
		next.Longitude = g.Longitude
		next.SpeedKph = g.SpeedMS * 3.6
		next.HeadingDeg = g.HeadingDeg
		next.AltitudeM = g.AltitudeM
		if g.TimestampMs > 0 {
			next.LocationFixAt = time.UnixMilli(g.TimestampMs)
		}
		m.putCacheLocked(vehicleID, &next)
		m.mu.Unlock()
		// Informational: how stale the vehicleState feed was when this
		// gnss frame landed — the old "gap-fill" signal, now just a
		// health metric for the demoted filler feed.
		if !lastVeh.IsZero() && time.Since(lastVeh) > parallaxGPSStallThreshold {
			m.logger.Info("parallax gps covering vehicleState stall", "vehicle", vehicleID,
				"vehicleState_gap_s", time.Since(lastVeh).Round(time.Second).Seconds())
		}
		m.record(ctx, vehicleID, prev, &next)
	})
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		m.logger.Warn("dynamics gnss subscription ended", "vehicle", vehicleID, "err", err.Error())
	}
}

// driveDynamicsSubscriber streams the Parallax dynamics.vehicle.{gear,
// drive_mode,odometer} + vehicle.power.state topics. It shadow-logs each
// decoded value next to the concurrent vehicleState reading and applies
// them authoritatively (see the parallaxDriveDynamics field doc): gear
// R/N/D opens/sustains a drive at its true start; odometer bridges a
// vehicleState stall monotonically; power.state/drive_mode fill the cache.
// It never applies Parallax P (mid-drive P blips would fragment) and never
// advances m.stamp (record() doesn't), so the vehicleState watchdog and
// drive-close authority are untouched. Gated on
// RIVOLT_PARALLAX_DRIVE_DYNAMICS + Parallax connectivity.
func (m *StateMonitor) driveDynamicsSubscriber(ctx context.Context, vehicleID string) {
	err := m.client.SubscribeDriveDynamics(ctx, vehicleID, m.captureTopics(vehicleID), func(f DriveDynamicsFrame) {
		m.onDriveDynamicsFrame(ctx, vehicleID, f)
	})
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		m.logger.Warn("drive-dynamics shadow subscription ended", "vehicle", vehicleID, "err", err.Error())
	}
}

// onDriveDynamicsFrame handles one frame of the drive-dynamics Parallax
// subscription. Values reach the live state only from fresh frames
// (applicable).
func (m *StateMonitor) onDriveDynamicsFrame(ctx context.Context, vehicleID string, f DriveDynamicsFrame) {
	fresh := m.noteParallaxFrame(vehicleID, f.RVM, f.TimestampMs)
	m.mu.Lock()
	prev := m.cache[vehicleID]
	m.mu.Unlock()
	var vehGear, vehPower string
	var vehOdoMi float64
	if prev != nil {
		vehGear = prev.Gear
		vehPower = prev.PowerState
		vehOdoMi = prev.OdometerKm * kmToMi
	}
	switch f.RVM {
	case rvmDriveGear:
		m.applyParallaxGear(ctx, vehicleID, f, fresh, vehGear)
	case rvmDriveMode:
		dm := driveModeFromParallax(f.Value)
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "drive_mode",
			"px_enum", f.Value, "px_mode", dm, "ts_ms", f.TimestampMs)
		// Apply the mapped drive mode to the cache.
		// Unmapped enums (unspecified/init/fault) fall back to vehicleState.
		// Like the gear, only from a fresh frame: a replay re-sends the
		// mode of the last change, however long ago.
		if m.parallaxDriveDynamics && dm != "" && m.applicable(f, fresh) {
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && base.DriveMode != dm {
				next := *base
				next.At = time.Now()
				next.DriveMode = dm
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmOdometer:
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "odometer",
			"px_km", f.Value, "px_mi", float64(f.Value)*kmToMi,
			"vehicleState_mi", vehOdoMi, "ts_ms", f.TimestampMs)
		// Monotonic stall-bridge: advance the cached odometer only when
		// Parallax reads higher than the cache. Never lowers it, so
		// vehicleState's finer 0.01-mi resolution wins in normal
		// operation and Parallax only fills a stall where vehicleState
		// has frozen.
		if m.parallaxDriveDynamics && f.ValueOK {
			km := float64(f.Value)
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && km > base.OdometerKm {
				next := *base
				next.At = time.Now()
				next.OdometerKm = km
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmPowerState:
		// px_raw_b64 kept in the log so the still-unmapped enums
		// (sleep, …) can be RE'd from the shadow logs.
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "power_state",
			"px_enum", f.Value, "px_enum_ok", f.ValueOK,
			"px_raw_b64", f.Payload, "px_power", powerStateFromParallax(f.Value),
			"vehicleState_power", vehPower, "ts_ms", f.TimestampMs)
		// Apply the mapped powerState to the cache (fresher than
		// vehicleState's push). Unmapped enums are left to vehicleState.
		// Only from a fresh frame: a replayed "ready" while the car
		// sleeps would read as a wake (sleep gate, rest_quiet).
		pxApplies := m.parallaxDriveDynamics && m.applicable(f, fresh)
		if m.parallaxDriveDynamics && !pxApplies {
			m.fillBlankPowerState(vehicleID, f)
		}
		if pxApplies {
			if ps := powerStateFromParallax(f.Value); ps != "" {
				m.mu.Lock()
				if m.lastPxPower == nil {
					m.lastPxPower = map[string]string{}
				}
				m.lastPxPower[vehicleID] = ps
				if base := m.cache[vehicleID]; base != nil && base.PowerState != ps {
					next := *base
					next.At = time.Now()
					next.PowerState = ps
					next.PowerStateAt = time.Time{}
					if f.TimestampMs > 0 {
						next.PowerStateAt = time.UnixMilli(f.TimestampMs).UTC()
					}
					m.putCacheLocked(vehicleID, &next)
				}
				m.mu.Unlock()
			}
		}
	case rvmRange:
		// distanceToEmpty in whole km (field 1) — State.DistanceToEmpty
		// is already km, so apply directly. Confirmed live: 308 km =
		// vehicleState 191.4 mi.
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "range",
			"px_km", f.Value, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && f.ValueOK && f.Value > 0 {
			km := float64(f.Value)
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && base.DistanceToEmpty != km {
				next := *base
				next.At = time.Now()
				next.DistanceToEmpty = km
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmTires:
		tires, ok := decodeTires(f.Payload)
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "tires",
			"px_ok", ok, "px_tires", tires, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && ok {
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil {
				next := *base
				next.At = time.Now()
				for _, t := range tires {
					switch t.Position {
					case 1:
						next.TirePressureFLBar = t.Bar
					case 2:
						next.TirePressureFRBar = t.Bar
					case 3:
						next.TirePressureRLBar = t.Bar
					case 4:
						next.TirePressureRRBar = t.Bar
					}
				}
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmCabinTemps:
		// Interior cabin temp (field 3, °C) — field 3 = 26.0 matched
		// vehicleState inside_temp_c. Applied to CabinTempC.
		temp, ok := decodeCabinTemp(f.Payload)
		// Field 2 is the exterior temperature in the app's schema. Logged, never
		// applied: vehicle_state.outside_temp_c is 0 on every row, and a wrong
		// value would reach the recaps that use it.
		out, outOK := decodeCabinOutside(f.Payload)
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "cabin_temp",
			"px_c", temp, "px_ok", ok, "px_outside_c", out, "px_outside_ok", outOK, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && ok && temp > -50 && temp < 80 {
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && base.CabinTempC != temp {
				next := *base
				next.At = time.Now()
				next.CabinTempC = temp
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmClosures:
		cl, ok := decodeInstanceStates(f.Payload)
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "closures",
			"px_ok", ok, "px_states", cl, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && ok {
			// known(inst) → (closed, present-with-known-status). status
			// 0/absent means the vehicle lacks that closure — leave
			// vehicleState.
			known := func(inst int) (bool, bool) {
				s, p := cl[inst]
				if !p || s == 0 {
					return false, false
				}
				return s == closureStatusClose, true
			}
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil {
				next := *base
				next.At = time.Now()
				d1, o1 := known(1)
				d2, o2 := known(2)
				d3, o3 := known(3)
				d4, o4 := known(4)
				if o1 && o2 && o3 && o4 {
					next.DoorsClosed = d1 && d2 && d3 && d4
				}
				if v, o := known(closInstFrunk); o {
					next.FrunkClosed = v
				}
				if v, o := known(closInstLiftgate); o {
					next.LiftgateClosed = v
				}
				if v, o := known(closInstTailgate); o {
					next.TailgateClosed = v
				}
				if v, o := known(closInstTonneau); o {
					next.TonneauClosed = v
				}
				// Windows ride on the closures topic as instances 12-15
				// (mapping confirmed live on the R1S — see the closInst*
				// consts). Same "open"/"closed"/"" string shape the UI
				// expects from vehicleState; only set when the instance
				// is present with a known status.
				win := func(inst int, field *string) {
					if s, p := cl[inst]; p && s != 0 {
						if s == closureStatusClose {
							*field = "closed"
						} else {
							*field = "open"
						}
					}
				}
				win(closInstWindowFL, &next.WindowFrontLeftClosed)
				win(closInstWindowFR, &next.WindowFrontRightClosed)
				win(closInstWindowRL, &next.WindowRearLeftClosed)
				win(closInstWindowRR, &next.WindowRearRightClosed)
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmLocks:
		lk, ok := decodeInstanceStates(f.Payload)
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "locks",
			"px_ok", ok, "px_states", lk, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && ok {
			// Locked = all four door locks present, known, and LOCKED.
			allLocked, all4 := true, true
			for inst := 1; inst <= 4; inst++ {
				s, p := lk[inst]
				if !p || s == 0 {
					all4 = false
					break
				}
				if s != lockStatusLocked {
					allLocked = false
				}
			}
			if all4 {
				m.mu.Lock()
				if base := m.cache[vehicleID]; base != nil {
					next := *base
					next.At = time.Now()
					next.Locked = allLocked
					m.putCacheLocked(vehicleID, &next)
				}
				m.mu.Unlock()
			}
		}
	case rvmAlarmState:
		// security.alarm.state field 1 = sound_alarm (APK enum). Applied
		// to AlarmSoundStatus; logs the concurrent vehicleState value so
		// the mapping can be verified against the legacy feed.
		ps := alarmSoundFromParallax(f.Value)
		var veh string
		if prev != nil {
			veh = prev.AlarmSoundStatus
		}
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "alarm",
			"px_enum", f.Value, "px_alarm", ps,
			"vehicleState_alarm", veh, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && ps != "" {
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && base.AlarmSoundStatus != ps {
				next := *base
				next.At = time.Now()
				next.AlarmSoundStatus = ps
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmLowVoltBatt:
		// energy.low_voltage.battery_state field 1 = 12V health (APK enum).
		ps := lowVoltHealthFromParallax(f.Value)
		var veh string
		if prev != nil {
			veh = prev.TwelveVoltBatteryHealth
		}
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "low_voltage_battery",
			"px_enum", f.Value, "px_health", ps,
			"vehicleState_health", veh, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && ps != "" {
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && base.TwelveVoltBatteryHealth != ps {
				next := *base
				next.At = time.Now()
				next.TwelveVoltBatteryHealth = ps
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmTrailer:
		// body.trailer.state field 1 = trailer_presence_status (APK enum).
		// No State destination field yet — decode-and-log only.
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "trailer",
			"px_enum", f.Value, "px_trailer", trailerPresenceFromParallax(f.Value),
			"ts_ms", f.TimestampMs)
	case rvmPreconStatus:
		// comfort.cabin.cabin_preconditioning_status — decoded per the APK
		// CABIN_PRECONDITIONING_STATE enum, but the topic's field number is
		// unconfirmed (never emitted during RE), so log-only until a live
		// preconditioning capture verifies it, then promote to apply.
		var veh string
		if prev != nil {
			veh = prev.CabinPreconditioningStatus
		}
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "cabin_precondition",
			"px_enum", f.Value, "px_precon", cabinPreconStateFromParallax(f.Value),
			"vehicleState_precon", veh, "px_raw_b64", f.Payload, "ts_ms", f.TimestampMs)
	case rvmPetMode:
		// comfort.cabin.pet_mode_status field 1 = PET_MODE_STATE (APK enum;
		// field 1 confirmed live: 08 02 = DISABLED). f.ValueOK gates it so
		// an absent field isn't read as OFF (enum 0).
		ps := petModeFromParallax(f.Value)
		var veh string
		if prev != nil {
			veh = prev.PetModeStatus
		}
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", "pet_mode",
			"px_enum", f.Value, "px_pet", ps,
			"vehicleState_pet", veh, "ts_ms", f.TimestampMs)
		if m.parallaxDriveDynamics && f.ValueOK {
			m.mu.Lock()
			if base := m.cache[vehicleID]; base != nil && base.PetModeStatus != ps {
				next := *base
				next.At = time.Now()
				next.PetModeStatus = ps
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
		}
	case rvmChargingStatus:
		m.applyParallaxChargingStatus(ctx, vehicleID, f, prev)
	case rvmTimeEstimation:
		m.applyParallaxTimeEstimation(vehicleID, f)
	case rvmOtaDeploy:
		m.applyParallaxOTA(vehicleID, f, prev)
	default:
		// The time-boxed next-topics capture (parallax_next.go): a field
		// tree, never the raw payload, and nothing once it has ended.
		if spec, ok := pxNextTopics[f.RVM]; ok {
			if pxNextCapturing(time.Now()) {
				m.logPxNext(vehicleID, spec, f)
			}
			return
		}
		// Any other not-yet-RE'd topic: log the raw payload + best-effort
		// varint so its wire shape can be decoded offline from the logs,
		// same as power.state/range/tires were. No cache apply.
		m.logger.Info("parallax drive-dynamics shadow",
			"vehicle", vehicleID, "topic", f.RVM,
			"px_enum", f.Value, "px_enum_ok", f.ValueOK,
			"px_raw_b64", f.Payload, "ts_ms", f.TimestampMs)
	}
}

// applyParallaxChargingStatus handles a charging.session.status frame. It
// shadow-logs the decoded plug and display status next to vehicleState's
// chargerStatus and chargerState (on an R1 that validates the mapping) and,
// for a vehicle whose vehicleState sends no chargerStatus (an R2), applies
// the plug status to the cache, marked ChargerStatusPx so the recorder keeps
// the power inference as the fallback. A plug status is a state, not an
// event: the cloud's replay of the latest frame applies too (after a restart
// it's the only frame until the plug changes), but an out-of-order older
// frame doesn't.
func (m *StateMonitor) applyParallaxChargingStatus(ctx context.Context, vehicleID string, f DriveDynamicsFrame, prev *State) {
	cs, ok := decodeChargingStatus(f.Payload)
	status := chargerStatusFromParallax(cs.Plug)
	var vehStatus, vehState string
	if prev != nil {
		vehStatus, vehState = prev.ChargerStatus, prev.ChargerState
	}
	m.mu.RLock()
	vehReports := m.vehChargerStatus[vehicleID]
	latest := f.TimestampMs > 0 && m.lastTopicTs[vehicleID+"|"+f.RVM] == f.TimestampMs
	m.mu.RUnlock()
	args := []any{"vehicle", vehicleID, "topic", "charging_status",
		"px_ok", ok, "px_plug", cs.Plug, "px_display", cs.Display, "px_evse", cs.EVSE,
		"px_charger_status", status, "px_charger_state", chargerStateFromParallax(cs.Display),
		"vehicleState_charger_status", vehStatus, "vehicleState_charger_state", vehState,
		"vehicleState_has_status", vehReports, "ts_ms", f.TimestampMs}
	m.logger.Info("parallax drive-dynamics shadow", args...)
	if !m.parallaxDriveDynamics || !ok || status == "" || vehReports || !latest {
		return
	}
	m.mu.Lock()
	base := m.cache[vehicleID]
	if base == nil || m.vehChargerStatus[vehicleID] || (base.ChargerStatus == status && base.ChargerStatusPx) {
		m.mu.Unlock()
		return
	}
	next := *base
	next.At = time.Now()
	next.ChargerStatus = status
	next.ChargerStatusPx = true
	m.putCacheLocked(vehicleID, &next)
	m.mu.Unlock()
	m.record(ctx, vehicleID, base, &next)
}

// applyParallaxTimeEstimation sets the time to full from a
// charging.session.time_estimation frame, for a charging vehicle whose
// vehicleState reports none (an R2's). It is marked TimeToEndSource
// "estimate"; a vehicleState value replaces it, and the end of the charge
// clears it (mergeState). A frame that is not the latest, or arrives while
// the vehicle is not charging, is ignored; a zero clears an estimate.
func (m *StateMonitor) applyParallaxTimeEstimation(vehicleID string, f DriveDynamicsFrame) {
	mins, ok := decodeTimeEstimation(f.Payload)
	m.mu.RLock()
	latest := f.TimestampMs > 0 && m.lastTopicTs[vehicleID+"|"+f.RVM] == f.TimestampMs
	m.mu.RUnlock()
	m.logger.Info("parallax drive-dynamics shadow",
		"vehicle", vehicleID, "topic", "charge_time_estimation",
		"px_ok", ok, "px_minutes", mins, "ts_ms", f.TimestampMs)
	if !m.parallaxDriveDynamics || !ok || !latest {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	base := m.cache[vehicleID]
	if base == nil || !chargingNow(base.ChargerState) {
		return
	}
	if base.TimeToEndMins > 0 && base.TimeToEndSource != timeToEndEstimate {
		return // the vehicle reports its own
	}
	want := math.Round(mins)
	if want == base.TimeToEndMins && (want == 0 || base.TimeToEndSource == timeToEndEstimate) {
		return
	}
	next := *base
	next.At = time.Now()
	next.TimeToEndMins = want
	next.TimeToEndSource = timeToEndEstimate
	if want == 0 {
		next.TimeToEndSource = ""
	}
	m.putCacheLocked(vehicleID, &next)
}

// timeToEndEstimate is State.TimeToEndSource for a Parallax-derived time to full.
const timeToEndEstimate = "estimate"

// chargingNow reports whether a chargerState is an active session.
func chargingNow(cs string) bool {
	return cs == "charging_active" || cs == "charging_connecting"
}

// applyParallaxOTA handles an ota.deployment.state frame: it logs the
// decoded installed and pending versions next to vehicleState's and, for a
// vehicle whose vehicleState sends no otaCurrentVersion (an R2), sets them
// on the cache. The R1s carry both, so the log validates the decode. Like
// the plug status it's a state: the replay of the latest frame applies, an
// out-of-order older one doesn't.
func (m *StateMonitor) applyParallaxOTA(vehicleID string, f DriveDynamicsFrame, prev *State) {
	cur, avail, ok := decodeOTADeployment(f.Payload)
	var vehCur, vehAvail string
	if prev != nil {
		vehCur, vehAvail = prev.OtaCurrentVersion, prev.OtaAvailableVersion
	}
	m.mu.RLock()
	vehReports := m.vehOtaVersion[vehicleID]
	latest := f.TimestampMs > 0 && m.lastTopicTs[vehicleID+"|"+f.RVM] == f.TimestampMs
	m.mu.RUnlock()
	args := []any{"vehicle", vehicleID, "topic", "ota_deployment",
		"px_ok", ok, "px_current", cur, "px_available", avail,
		"vehicleState_current", vehCur, "vehicleState_available", vehAvail,
		"vehicleState_has_version", vehReports, "ts_ms", f.TimestampMs}
	if !ok {
		args = append(args, "px_raw_b64", f.Payload)
	}
	m.logger.Info("parallax drive-dynamics shadow", args...)
	if !m.parallaxDriveDynamics || !ok || cur == "" || vehReports || !latest {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	base := m.cache[vehicleID]
	if base == nil || m.vehOtaVersion[vehicleID] || (base.OtaCurrentVersion == cur && base.OtaAvailableVersion == avail) {
		return
	}
	next := *base
	next.At = time.Now()
	next.OtaCurrentVersion, next.OtaAvailableVersion = cur, avail
	m.putCacheLocked(vehicleID, &next)
}

// parkableGear reports whether a Parallax P should replace the cached gear:
// a driving gear (vehicleState that never reports park, so the last D or R
// sticks) or none at all (an R2's vehicleState sends the gear blank, and
// after a restart the cache starts without one until a gear frame lands;
// stored and served blank, it reads as "unknown" rather than parked).
func parkableGear(g string) bool {
	return isDrivingGear(g) || strings.TrimSpace(g) == ""
}

// applicable reports whether a drive-dynamics value may be applied to
// the live state: a fresh frame (noteParallaxFrame) whose event time is
// within parallaxGearMaxAge. A replay is counted for the replay metric.
func (m *StateMonitor) applicable(f DriveDynamicsFrame, fresh bool) bool {
	ok := fresh && f.TimestampMs > 0 && time.Since(time.UnixMilli(f.TimestampMs)) <= parallaxGearMaxAge
	if !ok {
		m.mu.RLock()
		obs := m.sleepObserver
		m.mu.RUnlock()
		if o, isObs := obs.(ParallaxReplayObserver); isObs {
			o.OnParallaxReplayIgnored(f.RVM)
		}
	}
	return ok
}

// ParallaxReplayObserver is optionally implemented by the SleepObserver
// (main's metrics adapter): told each time a replayed or stale Parallax
// value is ignored instead of applied, per topic.
type ParallaxReplayObserver interface {
	OnParallaxReplayIgnored(topic string)
}

// fillBlankPowerState seeds an empty power state from a replayed or stale
// power.state frame. An R2's vehicleState reports no usable power state, and
// the cloud replays only the latest frame (the last change, possibly hours
// old) on each (re)subscribe, so until the next real change the state would
// stay blank: half of an R2's rows had none. The latest frame is the truck's
// current state, so it fills a blank; it never replaces a value the cache has
// (a replayed "ready" must not read as a wake over a "sleep"), it is not a
// fresh frame (the veto and corroboration state are untouched), and its event
// time is kept in PowerStateAt.
func (m *StateMonitor) fillBlankPowerState(vehicleID string, f DriveDynamicsFrame) {
	ps := powerStateFromParallax(f.Value)
	if ps == "" || f.TimestampMs <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	base := m.cache[vehicleID]
	if base == nil || strings.TrimSpace(base.PowerState) != "" {
		return
	}
	next := *base
	next.At = time.Now()
	next.PowerState = ps
	next.PowerStateAt = time.UnixMilli(f.TimestampMs).UTC()
	if m.lastPxPower == nil {
		m.lastPxPower = map[string]string{}
	}
	if m.lastPxPower[vehicleID] == "" {
		m.lastPxPower[vehicleID] = ps // so holdParallaxLocked can guard it
	}
	m.putCacheLocked(vehicleID, &next)
}

// applyParallaxGear handles one dynamics.vehicle.gear frame.
//
// A replayed frame (same or older ts_ms than one already seen, or an
// event time older than parallaxGearMaxAge) is IGNORED: it must not
// refresh the "fresh Parallax gear" veto/corroboration, must not be
// copied into the cached state and must not call record(). Without
// this gate every Parallax resubscribe (~3 min) replayed the last real
// gear (a D from repositioning before plugging in) into a car that was
// fast-charging, and the recorder closed and reopened the charge and a
// drive on every frame (2026-10-10, 01-000000006: 462 forced closes,
// 11 charge pieces with running energy totals).
func (m *StateMonitor) applyParallaxGear(ctx context.Context, vehicleID string, f DriveDynamicsFrame, fresh bool, vehGear string) {
	g := gearFromParallax(f.Value)
	if !m.applicable(f, fresh) {
		m.logger.Debug("parallax gear frame ignored (replay)",
			"vehicle", vehicleID, "px_enum", f.Value, "px_gear", g,
			"vehicleState_gear", vehGear, "ts_ms", f.TimestampMs)
		// One exception: a replayed P is the last gear the car reported,
		// so a car standing still while the cache reads driving or has no
		// gear at all (a vehicleState that never reports park, after a restart or a
		// missed park frame) is parked. It doesn't count as fresh: the
		// close corroboration still needs a stop (speed) or time.
		if g == "P" && m.parallaxDriveDynamics && f.TimestampMs > 0 {
			m.mu.Lock()
			base := m.cache[vehicleID]
			if base != nil && parkableGear(base.Gear) && base.SpeedKph*kphToMi <= driveCloseSpeedMph {
				m.lastPxGear[vehicleID] = "P"
				next := *base
				next.At = time.Now()
				next.Gear = "P"
				m.putCacheLocked(vehicleID, &next)
				m.mu.Unlock()
				m.record(ctx, vehicleID, base, &next)
				return
			}
			m.mu.Unlock()
		}
		return
	}
	m.logger.Info("parallax drive-dynamics shadow",
		"vehicle", vehicleID, "topic", "gear",
		"px_enum", f.Value, "px_gear", g,
		"vehicleState_gear", vehGear, "ts_ms", f.TimestampMs)
	// Feed the drive-close debounce: fresh Parallax P corroborates
	// a close, fresh R/N/D vetoes a vehicleState P blip.
	if g != "" {
		m.mu.Lock()
		m.lastPxGear[vehicleID] = g
		m.lastPxGearAt[vehicleID] = time.Now()
		m.mu.Unlock()
	}
	// Authoritative early-open: a Parallax driving gear (R/N/D)
	// opens/sustains the drive now. Parallax leads vehicleState by
	// seconds (tens of seconds when vehicleState stalls), so this
	// captures the true start — and, because the cache still holds
	// the pre-drive parked odometer/location, the correct start
	// odometer and start point.
	//
	// A fresh P is applied too, when the cached gear still reads
	// driving: some vehicles' vehicleState never reports park (R2: the
	// gear stays at the last D/R for days), so without it the drive
	// never closes and no charge opens. The close itself stays with the
	// recorder's debounce (a fresh Parallax P corroborates it), and
	// holdParallaxLocked keeps vehicleState replays of the stale gear
	// from undoing it while the car stands still.
	if m.parallaxDriveDynamics && (isDrivingGear(g) || g == "P") {
		m.mu.Lock()
		base := m.cache[vehicleID]
		if base != nil && (g != "P" || parkableGear(base.Gear)) {
			next := *base
			next.At = time.Now()
			next.Gear = g
			m.putCacheLocked(vehicleID, &next)
			m.mu.Unlock()
			m.record(ctx, vehicleID, base, &next)
		} else {
			m.mu.Unlock()
		}
	}
}

// applyVehicleStatePush merges one vehicleState subscription push into
// the cache and runs the recorder on it.
func (m *StateMonitor) applyVehicleStatePush(ctx context.Context, vehicleID string, st *State) {
	// When Parallax GPS is authoritative, drop vehicleState's GPS
	// before it reaches the cache so the gnss subscriber is the only
	// writer of lat/lon/speed/heading/altitude.
	m.noteVehStateGPS(vehicleID, st)
	m.mu.Lock()
	// Rivian pushes deltas — each frame contains only the fields that
	// changed. Merge non-zero/non-empty values from the push over
	// whatever we had cached so static fields (odometer, gear, charge
	// limit, tire pressures) don't disappear between frames.
	prev := m.cache[vehicleID]
	if st != nil && strings.TrimSpace(st.ChargerStatus) != "" {
		m.vehChargerStatus[vehicleID] = true
	}
	if st != nil && strings.TrimSpace(st.OtaCurrentVersion) != "" {
		m.vehOtaVersion[vehicleID] = true
	}
	merged := mergeState(prev, st)
	m.holdParallaxLocked(vehicleID, prev, merged)
	m.putCacheLocked(vehicleID, merged)
	m.stamp[vehicleID] = time.Now()
	// Cold-start guard: any real WS frame proves the subscription is
	// delivering, so adaptiveRefreshInterval can return to
	// PowerState-driven cadence.
	m.noteVehicleStateFrameLocked(vehicleID)
	m.mu.Unlock()
	m.record(ctx, vehicleID, prev, merged)
}

// parallaxLiveWindow: Parallax counts as live for holdParallaxLocked when
// any of its topics delivered a frame this recently (replays included:
// they prove the subscription is up, not that anything changed).
const parallaxLiveWindow = 10 * time.Minute

// holdParallaxLocked keeps what fresh Parallax frames established
// against a vehicleState push that would undo it with a stale value
// (every resubscribe replays vehicleState's last snapshot):
//   - a Parallax P against a vehicleState driving gear, while the car
//     stands still (speed at or under driveCloseSpeedMph). A real drive
//     start shows as a fresh Parallax R/D first, or as speed;
//   - a Parallax sleep against a vehicleState "ready" (an R2 reports
//     only ready/go/blank), while Parallax is live; "go" always wins.
//
// prev is the cached state the push merged onto; merged is changed in
// place. Caller holds mu.
func (m *StateMonitor) holdParallaxLocked(vehicleID string, prev, merged *State) {
	if !m.parallaxDriveDynamics || prev == nil || merged == nil {
		return
	}
	if prev.Gear == "P" && isDrivingGear(merged.Gear) && m.lastPxGear[vehicleID] == "P" &&
		merged.SpeedKph*kphToMi <= driveCloseSpeedMph {
		merged.Gear = "P"
	}
	if prev.PowerState == "sleep" && merged.PowerState == "ready" && m.lastPxPower[vehicleID] == "sleep" &&
		time.Since(m.lastParallaxAt[vehicleID]) < parallaxLiveWindow {
		merged.PowerState, merged.PowerStateAt = prev.PowerState, prev.PowerStateAt
	}
}

// bondCharge stamps the recorder's currently-open liveCharge id so
// the next applyLiveSession push gets bonded to it. Called by the
// recorder on charge-open, paired with delete(m.chargeBond, ...) on
// close. Without a bond, upsertLiveCharge refuses to credit cached
// LiveSession energy/power fields to the row -- closing the
// "between-sessions Parallax replay leaks energy" hole.
func (m *StateMonitor) bondCharge(vehicleID, chargeID string) {
	m.mu.Lock()
	m.chargeBond[vehicleID] = chargeID
	m.mu.Unlock()
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
		if prev.RivianChargerKnown {
			sess.RivianChargerKnown = true
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
		if sess.Network == "" {
			sess.Network = prev.Network
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
	// Bond this cached session to whichever liveCharge the recorder
	// currently has open. Empty bond means "no charge open right now"
	// -- a stale Parallax replay arriving between sessions still
	// updates lastSession (other code paths consume it), but
	// upsertLiveCharge will refuse to credit its energy / power
	// fields to a future session that opens with a different bond.
	m.lastSessionFor[vehicleID] = m.chargeBond[vehicleID]
	prev := m.cache[vehicleID]
	var merged *State
	if prev != nil {
		cp := *prev
		if sess.PowerKW > 0 {
			cp.ChargerPowerKW = sess.PowerKW
		}
		// sess.PowerKW falls back to the previous push's value, so a
		// replayed frame after the session ended would put the old
		// power straight back. Only an actively charging car draws.
		dropIdleChargerPower(&cp)
		merged = &cp
		m.putCacheLocked(vehicleID, merged)
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
	// LocationFixAt: take the newer non-zero value. Push deltas
	// without a GNSSLocation block leave the field zero \u2014 don't
	// overwrite a known-good prior fix with that.
	if !next.LocationFixAt.IsZero() {
		out.LocationFixAt = next.LocationFixAt
	}

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
	// Pack temps arrive on a separate Parallax topic, not in the
	// vehicleState push — carry them across frames so every recorded
	// row keeps the last-known reading until the next battery_state
	// frame refreshes it.
	mergeFloat(&out.PackTempAvgC, next.PackTempAvgC)
	mergeFloat(&out.PackTempMaxC, next.PackTempMaxC)
	mergeFloat(&out.PackTempMinC, next.PackTempMinC)
	// Time to full: the vehicle's own value wins over a Parallax estimate;
	// an estimate ends with the charge.
	if next.TimeToEndMins > 0 {
		out.TimeToEndMins, out.TimeToEndSource = next.TimeToEndMins, ""
	}

	// Strings: non-empty wins.
	mergeString(&out.Gear, next.Gear)
	mergeString(&out.DriveMode, next.DriveMode)
	mergeString(&out.ChargerState, next.ChargerState)
	mergeString(&out.ChargerStatus, next.ChargerStatus)
	if next.ChargingSchedules != nil {
		out.ChargingSchedules = next.ChargingSchedules
	}
	if next.ChargerStatus != "" {
		out.ChargerStatusPx = next.ChargerStatusPx
	}
	mergeString(&out.ChargePortState, next.ChargePortState)
	mergeString(&out.RemoteChargingAvailable, next.RemoteChargingAvailable)
	mergeString(&out.CabinPreconditioningStatus, next.CabinPreconditioningStatus)
	mergeString(&out.PowerState, next.PowerState)
	if next.PowerState != "" {
		out.PowerStateAt = next.PowerStateAt
	}
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
	dropIdleChargerPower(&out)
	if out.TimeToEndSource == timeToEndEstimate && !chargingNow(out.ChargerState) {
		out.TimeToEndMins, out.TimeToEndSource = 0, ""
	}
	return &out
}

// chargerDrawing reports whether a Rivian chargerState means the pack
// is taking power right now. charging_ready (plugged in, waiting on a
// schedule or the limit), charging_complete, and the stopped/error
// states all draw nothing, even with the cable connected.
func chargerDrawing(cs string) bool {
	switch strings.ToLower(strings.TrimSpace(cs)) {
	case "charging_active", "charging_connecting":
		return true
	}
	return false
}

// dropIdleChargerPower zeroes ChargerPowerKW once the car reports a
// known charger state that isn't drawing.
//
// Charger power only ever arrives on charging-session pushes, and
// mergeFloat's "non-zero wins" rule meant nothing ever cleared it: the
// last session's power (e.g. 7.5 kW on a home wall charger) rode along
// on every sample until the pod restarted, for weeks at a time. That
// made SleepActivity count all parked time as charging (idle-awake
// read zero), stamped zero-length "charges" with the stale peak, and
// let the recorder integrate phantom energy through charging_ready
// stretches. An empty state means "not reported in this frame", so it
// leaves the value alone.
func dropIdleChargerPower(s *State) {
	if s.ChargerState != "" && !chargerDrawing(s.ChargerState) {
		s.ChargerPowerKW = 0
	}
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

// liveStateSnapshotInterval is how often the lease-owning pod
// republishes its merged live State to the shared store so peer replicas
// can serve a complete /api/state. Kept well under LiveStateSnapshotTTL
// so the snapshot never expires between refreshes on a healthy owner.
// A var so tests can shrink it.
var liveStateSnapshotInterval = 5 * time.Second

// RemoteLatest returns the live State a peer pod published to the shared
// store for this vehicle, or nil when none is available (no shared store
// wired, the owner hasn't published yet, or the snapshot aged out). Used
// by the /api/state read path on a replica that does NOT own the
// vehicle's WS lease: its local cache is empty and a direct REST fetch
// would omit every subscription-only field (pack temperature, numeric
// tire pressures, charging context, driver chips, windows). Reading the
// owner's published snapshot serves those fields pod-agnostically.
//
// A snapshot read is reused for remoteLatestTTL: the owner republishes
// only every liveStateSnapshotInterval, so /now and /api/state polls (the
// public demo's visitors all poll the same vehicle) share one store read
// instead of one each. Misses aren't cached.
func (m *StateMonitor) RemoteLatest(ctx context.Context, vehicleID string) *State {
	if m == nil || m.liveStateStore == nil {
		return nil
	}
	m.remoteMu.Lock()
	if e, ok := m.remoteLatest[vehicleID]; ok && time.Since(e.at) < remoteLatestTTL {
		m.remoteMu.Unlock()
		cp := *e.st
		return &cp
	}
	m.remoteMu.Unlock()
	st, err := m.liveStateStore.GetState(ctx, vehicleID)
	if err != nil {
		m.logger.Debug("remote live state get failed", "vehicle", vehicleID, "err", err.Error())
		return nil
	}
	if st == nil {
		return nil
	}
	m.remoteMu.Lock()
	if m.remoteLatest == nil {
		m.remoteLatest = make(map[string]remoteLatestEntry)
	}
	m.remoteLatest[vehicleID] = remoteLatestEntry{st: st, at: time.Now()}
	m.remoteMu.Unlock()
	cp := *st
	return &cp
}

// remoteLatestTTL is how long RemoteLatest reuses a snapshot it read:
// well under liveStateSnapshotInterval, so it adds at most that much
// staleness to a peer's view.
var remoteLatestTTL = 2 * time.Second

type remoteLatestEntry struct {
	st *State
	at time.Time
}

// stateRefetchEvery bounds how often /api/state on a pod that doesn't
// stream a vehicle may fetch it directly from Rivian when the owner's
// published snapshot is missing (no shared store, Redis down, a handover
// gap). Between fetches it serves the entry it has, however old.
const stateRefetchEvery = 5 * time.Minute

// ServeState decides what /api/state serves for vehicleID. It returns
// the state to serve and whether the caller should first fetch it
// directly (then Prime the result, or on failure serve st if non-nil):
//
//  1. this pod streams the vehicle: its own cache, the freshest there is;
//  2. otherwise the owner's published snapshot (RemoteLatest);
//  3. otherwise the local entry (a past fetch, or the last frame before
//     this pod lost the lease), with fetch=true at most once per
//     stateRefetchEvery per vehicle on this pod, and never while that
//     entry says the vehicle is asleep: this path has no sleep gate of
//     its own, and a Redis outage must not fan out into Rivian calls;
//  4. no entry: fetch=true under the same limit, else (nil, false).
//
// Serving the local entry first, as Latest-first did, froze /api/state
// on a non-owner pod from its first fallback fetch until it restarted.
func (m *StateMonitor) ServeState(ctx context.Context, vehicleID string) (st *State, fetch bool) {
	if m == nil {
		return nil, true
	}
	m.mu.RLock()
	local := m.cache[vehicleID]
	_, streaming := m.active[vehicleID]
	m.mu.RUnlock()
	if streaming && local != nil {
		return local, false
	}
	if remote := m.RemoteLatest(ctx, vehicleID); remote != nil {
		return remote, false
	}
	if local != nil && strings.EqualFold(strings.TrimSpace(local.PowerState), "sleep") {
		return local, false
	}
	m.fallbackMu.Lock()
	defer m.fallbackMu.Unlock()
	if m.fallbackAt == nil {
		m.fallbackAt = make(map[string]time.Time)
	}
	if last, ok := m.fallbackAt[vehicleID]; ok && time.Since(last) < stateRefetchEvery {
		return local, false
	}
	// Claimed before the fetch, so concurrent polls serve local (or wait
	// out the window) instead of fetching too.
	m.fallbackAt[vehicleID] = time.Now()
	return local, true
}

// liveStatePublisher periodically writes this vehicle's cached State to
// the shared store so a replica that doesn't own the lease can serve it
// via RemoteLatest. Runs only on the owning pod — it's started from
// run(), which executes only for subscribed vehicles — and is a no-op
// when no shared store is wired (single-binary / no-Redis dev). Errors
// are best-effort: a Redis blip just means peers briefly fall back to
// REST.
func (m *StateMonitor) liveStatePublisher(ctx context.Context, vehicleID string) {
	if m.liveStateStore == nil {
		return
	}
	t := time.NewTicker(liveStateSnapshotInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.RLock()
			cached := m.cache[vehicleID]
			frameAt := m.vehicleStateFrameAtLocked(vehicleID)
			m.mu.RUnlock()
			if cached == nil {
				continue
			}
			snap := *cached // value copy; publish outside the lock
			snap.VehicleStateFrameAt = frameAt
			pctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := m.liveStateStore.PutState(pctx, vehicleID, &snap, LiveStateSnapshotTTL); err != nil {
				m.logger.Debug("live state publish failed", "vehicle", vehicleID, "err", err.Error())
			}
			cancel()
		}
	}
}

// noteVehicleStateFrameLocked records that a vehicleState frame just
// arrived: the feed is delivering (wsSeen) and when. Caller holds mu.
func (m *StateMonitor) noteVehicleStateFrameLocked(vehicleID string) {
	m.wsSeen[vehicleID] = true
	if m.lastVSFrame == nil {
		m.lastVSFrame = make(map[string]time.Time)
	}
	m.lastVSFrame[vehicleID] = time.Now()
}

// vehicleStateFrameAtLocked is VehicleStateFrameAt for a vehicle this
// pod streams: nil while no live feed is expected (no frame yet, or
// vehicleState held while asleep). "No frame yet" is this ownership's:
// wsSeen is cleared when the vehicle is forgotten (lease lost), so a pod
// that re-acquires it reports nil until its own first frame instead of
// the previous ownership's time. lastVSFrame itself is kept (#129's
// guard reads it). Caller holds mu (read or write).
func (m *StateMonitor) vehicleStateFrameAtLocked(vehicleID string) *time.Time {
	t, ok := m.lastVSFrame[vehicleID]
	if !ok || t.IsZero() || !m.wsSeen[vehicleID] || m.holdingVS[vehicleID] {
		return nil
	}
	t = t.UTC()
	return &t
}

// VehicleStateFrameAt is when the vehicleState feed last delivered a
// frame for the vehicle (GET /now's vehicle_state_frame_at), or nil
// when there is no live feed to judge: this pod doesn't stream the
// vehicle and the owner hasn't published one, the subscription hasn't
// delivered yet, or it is held while the car sleeps (wake-safety
// HoldAsleep). A non-nil time that's old means the feed is stuck. A pod
// that doesn't stream the vehicle reads the owner's published snapshot
// (refreshed every liveStateSnapshotInterval; gone after
// LiveStateSnapshotTTL once the owner stops).
func (m *StateMonitor) VehicleStateFrameAt(ctx context.Context, vehicleID string) *time.Time {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	_, streaming := m.active[vehicleID]
	var at *time.Time
	if streaming {
		at = m.vehicleStateFrameAtLocked(vehicleID)
	}
	m.mu.RUnlock()
	if streaming {
		return at
	}
	if remote := m.RemoteLatest(ctx, vehicleID); remote != nil {
		return remote.VehicleStateFrameAt
	}
	return nil
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
	distance := driveDistanceMi(&d)
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

// putCacheLocked replaces the cached State for vehicleID, stamping
// wokeAt when it takes PowerState from sleep to anything else. Caller
// holds mu.
func (m *StateMonitor) putCacheLocked(vehicleID string, next *State) {
	if prev := m.cache[vehicleID]; prev != nil && next != nil &&
		isSleepPS(prev.PowerState) && !isSleepPS(next.PowerState) && strings.TrimSpace(next.PowerState) != "" {
		if m.wokeAt == nil {
			m.wokeAt = make(map[string]time.Time)
		}
		m.wokeAt[vehicleID] = time.Now()
	}
	m.cache[vehicleID] = next
}

func isSleepPS(ps string) bool { return strings.EqualFold(strings.TrimSpace(ps), "sleep") }

// Prime stores a state from an out-of-band source (typically a REST
// fallback on first request) so subsequent Latest() calls return it
// immediately while the subscription is still spinning up.
func (m *StateMonitor) Prime(vehicleID string, st *State) {
	if st == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putCacheLocked(vehicleID, st)
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
	if m.client == nil {
		// Source-fed monitor: metadata was seeded at construction.
		return nil
	}
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
	refreshed := make([]Vehicle, 0, len(vehicles))
	for i := range vehicles {
		v := vehicles[i]
		if url, ok := heroByVehicle[v.ID]; ok {
			v.ImageURL = url
		}
		if list, ok := imagesByVehicle[v.ID]; ok {
			v.Images = list
		}
		m.vehicleInfo[v.ID] = &v
		refreshed = append(refreshed, v)
	}
	hook := m.vehicleInfoHook
	m.mu.Unlock()
	// Persist what was just fetched, outside the lock: no extra Rivian
	// call, so vehicles.trim / image_url fill from the same two requests.
	if hook != nil && len(refreshed) > 0 {
		hook(refreshed)
	}
	return nil
}

// SetVehicleInfoHook wires the persister RefreshVehicleInfo calls with the
// vehicles it just cached. nil disables. Safe to call before Start.
func (m *StateMonitor) SetVehicleInfoHook(h func([]Vehicle)) {
	m.mu.Lock()
	m.vehicleInfoHook = h
	m.mu.Unlock()
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
	v := m.vehicleInfo[vehicleID]
	changed := false
	switch {
	case v == nil:
		// First touch — create a stub so PackKWhFor can see the value
		// even before RefreshVehicleInfo has run. Other fields stay
		// zero/empty and get filled in by the later refresh.
		m.vehicleInfo[vehicleID] = &Vehicle{ID: vehicleID, PackKWh: kwh}
		changed = true
	case v.PackKWh != kwh:
		cp := *v
		cp.PackKWh = kwh
		m.vehicleInfo[vehicleID] = &cp
		changed = true
	}
	hook := m.batteryCapacityHook
	m.mu.Unlock()
	// Fire the persister outside the lock so DB latency can't stall
	// the recorder hot path. The hook is keyed by rivian_vehicle_id
	// + the user_id captured in the closure at SetBatteryCapacityHook
	// time; it write-throughs to vehicles.pack_kwh so a future
	// process restart (which drops vehicleInfo) sees the right value.
	if changed && hook != nil {
		hook(vehicleID, kwh)
	}
}

// SetBatteryCapacityHook wires a write-through callback fired
// whenever observeBatteryCapacity records a new pack capacity for
// a vehicle. The hook is called outside the monitor lock and
// MUST be non-blocking — callers typically dispatch a goroutine
// that performs the DB UPDATE.
func (m *StateMonitor) SetBatteryCapacityHook(hook func(vehicleID string, kwh float64)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batteryCapacityHook = hook
}

// waitAuthReady blocks until the wrapped client reports it has a
// usable session, ctx is cancelled, or the active goroutine entry
// for vehicleID has been removed (Unsubscribe). Returns true when
// the caller may proceed to subscribe; false means tear-down.
//
// This replaces the prior tight-loop behavior where every per-vehicle
// goroutine called SubscribeVehicleState → got ErrNotAuthenticated →
// slept ~backoff → repeat, indefinitely, until a UI request happened
// to trigger the lazy hydrate middleware. After a pod restart the
// outcome was: telemetry collection silently parked until someone
// opened the app.
func (m *StateMonitor) waitAuthReady(ctx context.Context, vehicleID string) bool {
	if m.client == nil {
		return ctx.Err() == nil
	}
	if m.client.Authenticated() {
		return true
	}
	ready := m.client.AuthReady()
	m.logger.Info("rivian ws subscribe blocked: awaiting auth", "vehicle", vehicleID)
	select {
	case <-ctx.Done():
		m.releaseActive(ctx, vehicleID)
		return false
	case <-ready:
		return true
	}
}

// reauthPollInterval is how often a parked monitor re-checks whether the
// user's needs_reauth flag has cleared (via a fresh Login or the admin
// RefreshSession). Polling is cheap and avoids the alternative: opening
// a doomed WebSocket every wsStaleThreshold just to watch it go zombie.
// A var, not a const, so tests can shorten it.
var reauthPollInterval = time.Minute

// waitReauthClear parks the subscribe loop while the client is flagged
// needs_reauth. A flagged session has an expired userSessionToken that
// Rivian no longer feeds, so subscribing under it only produces a
// 10-minute zombie WS — repeated forever, that's continuous churn
// against the gateway for every stuck user. Instead we wait for the
// flag to clear (the user re-authenticates, or an admin runs
// RefreshSession). Returns false when ctx is cancelled (monitor
// shutting down or vehicle unsubscribed).
func (m *StateMonitor) waitReauthClear(ctx context.Context, vehicleID string) bool {
	if m.client == nil {
		return ctx.Err() == nil
	}
	if needs, _ := m.client.NeedsReauth(); !needs {
		return true
	}
	m.logger.Info("rivian ws subscribe parked: needs re-auth", "vehicle", vehicleID)
	t := time.NewTicker(reauthPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.releaseActive(ctx, vehicleID)
			return false
		case <-t.C:
			if needs, _ := m.client.NeedsReauth(); !needs {
				m.logger.Info("rivian ws subscribe resumed: re-auth cleared", "vehicle", vehicleID)
				return true
			}
		}
	}
}

// waitStaggerSlot blocks until this monitor's (possibly shared) Stagger hands
// out a slot.
func (m *StateMonitor) waitStaggerSlot(ctx context.Context) {
	m.stagger.Wait(ctx)
}

// SetStagger replaces the monitor's slot clock; the registry passes one shared
// instance to every monitor. Call before Start.
func (m *StateMonitor) SetStagger(s *Stagger) {
	if s != nil {
		m.stagger = s
	}
}

// jitter returns d randomized to [d/2, d*3/2). Used for reconnect
// backoff so concurrent subscriptions that died from the same
// upstream blip don't reconnect in lockstep — that's the literal
// definition of a thundering herd.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := int64(d / 2)
	return time.Duration(half + rand.Int64N(2*half+1))
}
