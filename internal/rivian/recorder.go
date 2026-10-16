package rivian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// haversineMeters is the great-circle distance between two lat/lon
// points on a spherical earth. Used by the GPS-gap fill heuristic to
// decide whether the jump from the last good fix to the new one is
// big enough to be worth route-snapping.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(lat2 - lat1)
	dLon := rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}

// Conversion factors between the wire (metric) and the samples store
// (imperial, inherited from the ElectraFi importer schema).
const (
	kmToMi  = 0.621371
	kphToMi = 0.621371
)

// Stale-session guards. A live session that's been "open" without a
// frame for longer than these gaps almost certainly straddles a real
// session boundary the recorder missed (Rivian's chargerState sticks
// at charging_ready/active for hours after unplug; WS drops; etc.),
// so the next incoming frame is treated as a NEW session instead of
// extending the old one. Same applies if SoC drops mid-charge — a
// real charge can't go backwards, so the user must have unplugged
// and driven between frames.
const (
	liveChargeMaxGap        = 30 * time.Minute
	liveChargeMaxSoCDropPct = 2.0
	liveDriveMaxGap         = 30 * time.Minute

	// Drive-close debounce. vehicleState can emit a phantom P for a
	// frame or two mid-drive (observed 2026-07-14: P at 5 mph for 2-9s,
	// then D again) and closing on a single frame split real trips.
	// A P frame closes the drive immediately only when the stop is
	// corroborated (speed at/near zero, or Parallax gear agreeing on
	// P); otherwise P must persist for driveCloseDebounce — which also
	// covers the blackout case where the parking frame carries a stale
	// speed reading.
	driveCloseDebounce = 10 * time.Second
	driveCloseSpeedMph = 2.0

	// Merge-on-reopen. A drive that reopens within driveReopenWindow of
	// the previous close, with the odometer having barely moved, is the
	// same trip interrupted by a blip (or a parking-lot shuffle): resume
	// the previous accumulator/row instead of fragmenting into a stub.
	driveReopenWindow = 60 * time.Second
	driveReopenMaxMi  = 0.05

	// chargingPowerFloorKW is the minimum charger power that counts as
	// "actively charging" for the energy integral and meaningful-frame
	// tracking. Below this, Rivian is reporting parasitic / thermal /
	// cabin pre-cond draw while plugged in but idle, not real charging.
	chargingPowerFloorKW = 0.3

	// maxLivePowerKW is a hard physical ceiling on per-frame charger
	// power. Rivian packs accept ~220 kW on the highest-trim DCFC and
	// the largest CCS stations top out around 350 kW. Anything above
	// this is a corrupted Parallax frame (we've seen ~90 MW from a
	// single bad float) and must be dropped, not clamped — clamping
	// would silently ratchet the session peak to 400 kW. We discard
	// the frame instead so the running max is unaffected.
	maxLivePowerKW = 400.0

	// l2PowerThresholdKW separates AC home charging from DC fast
	// charging for the live power-integral fallback. J1772 / NACS AC
	// tops out at 19.2 kW; the lowest DCFC stations start around 25 kW.
	// Sessions whose peak stayed below this band are treated as L2 and
	// allowed to use the integrated energy when Parallax stays silent.
	l2PowerThresholdKW = 22.0

	// maxIntegrationGap caps the dt fed into the trapezoidal energy
	// integral. A WS dropout that goes silent for ten minutes shouldn't
	// inflate the integral by ten minutes × last-known power — frame
	// cadence is normally seconds, so a multi-minute gap is a signal
	// we don't know what happened in between. Cap dt so the integral
	// undercounts during silence rather than over.
	maxIntegrationGap = 2 * time.Minute
)

// Pack size for the SoC-delta energy fallback is looked up
// per-vehicle via StateMonitor.PackKWhFor; see vehicle_info.go for
// the model/trim → kWh table.

// liveSessions tracks in-flight drive and charge session accumulators
// for a single vehicle. Each transition from a "not driving" to a
// "driving" gear opens a liveDrive; transitioning back to P closes
// it and upserts into drives.Store. Same for chargerState → charging
// → disconnected/complete transitions.
type liveSessions struct {
	drive  *liveDrive
	charge *liveCharge

	// lastClosed retains the most recently debounce-closed drive so a
	// reopen within driveReopenWindow (with the odometer barely moved)
	// resumes it instead of fragmenting the trip. Deliberately NOT set
	// by applyMutualExclusion's charge-forced close — a drive ended by
	// plugging in must not be resumable. lastClosedAt is the frame time
	// of the close decision (endAt can be minutes earlier after a
	// telemetry blackout).
	lastClosed   *liveDrive
	lastClosedAt time.Time

	// Running counters used as samples.Sample.DriveNumber /
	// ChargeNumber. Incremented at each session open so dashboards can
	// group vehicle_state rows by counter without colliding with
	// electrafi-imported numbers (which are scoped per-export).
	driveCounter  int64
	chargeCounter int64
}

type liveDrive struct {
	// rejected is set when the database's live-session overlap guard
	// (migration 0041) refused this drive: another recorder already wrote
	// an overlapping drive for the vehicle. The drive keeps accumulating
	// and retries with a trimmed start (rejected_session.go); final marks
	// the close write, which always retries.
	rejected   bool
	rejectedAt time.Time
	final      bool
	trim       *sessionTrim
	id         string
	startedAt  time.Time
	number     int64
	startSoC   float64
	startOdoMi float64
	startLat   float64
	startLon   float64
	maxSpeed   float64 // mph
	sumSpeed   float64 // mph
	speedN     int

	// Rolling "current end" so we can upsert as the drive grows. Each
	// state update refreshes these; the final values are what lands in
	// the drives table if the process dies mid-session.
	endAt    time.Time
	endSoC   float64
	endOdoMi float64
	endLat   float64
	endLon   float64

	// lastFixAt is the wall-clock time of the most recently appended
	// path point. Tracked separately from endAt because endAt updates
	// on every frame regardless of GPS validity; this only moves when
	// path actually grows. Used to detect lag windows long enough to
	// route-fill instead of leaving a straight-line shortcut.
	lastFixAt time.Time

	// lastFixTS is the GNSS timestamp (State.LocationFixAt) of the most
	// recently accepted path point. Parallax gnss is the GPS backbone
	// and vehicleState fills detail between its ticks — this enforces
	// fix-timestamp monotonicity so a stale vehicleState replay (the
	// gateway re-sends an old fix) can't drag the path backwards after
	// a newer Parallax fix has landed.
	lastFixTS time.Time

	// pendingCloseAt is when the first uncorroborated P frame arrived.
	// Zero while driving; the close branch only commits once the stop
	// is corroborated or P has persisted past driveCloseDebounce.
	pendingCloseAt time.Time

	// path is the accumulated GPS trace for this drive, one [lat, lon]
	// pair per frame that carried a usable fix. Encoded with the
	// Google polyline algorithm and persisted on every upsert so a
	// crash mid-drive still leaves a real route on the row, not a
	// straight line. We deliberately do NOT thin / Douglas-Peucker the
	// path here -- the recorder's own frame cadence (∼one every few
	// seconds, throttled by Rivian's WS push frequency) already keeps
	// the trace short, and we'd rather store the raw points and
	// simplify in the renderer if it ever becomes a problem.
	path [][2]float64
}

type liveCharge struct {
	// rejected, rejectedAt, final, trim: see liveDrive.
	rejected   bool
	rejectedAt time.Time
	final      bool
	trim       *sessionTrim
	id         string
	startedAt  time.Time
	number     int64
	startSoC   float64
	startOdoMi float64 // odometer when the charge opened; 0 if unknown
	lat        float64
	lon        float64
	maxPower   float64 // kW

	endAt      time.Time
	endSoC     float64
	finalState string

	// Running trapezoidal integral of ChargerPowerKW over wall-clock
	// time, in kWh. Used as the L2 energy fallback when Parallax never
	// emits a TotalChargedEnergyKWh — Rivian's vehicleState feed pushes
	// chargerPowerKW for L2 sessions even though Parallax stays silent.
	energyIntKWh float64
	lastPowerKW  float64
	lastPowerAt  time.Time

	// activeSeconds is the wall-clock time the session actually spent
	// charging (power >= chargingPowerFloorKW), accumulated alongside the
	// energy integral with the same idle-skip and gap-cap. Distinct from
	// endAt - startedAt, which spans the whole plugged-in period
	// including charging_ready / battery-conditioning idle. Persisted so
	// the UI can show real charging time instead of time-plugged-in.
	activeSeconds float64

	// dcfcPriced caches the fast-charge network price once resolved, so
	// the per-frame upserts of an open session don't re-run the station
	// lookup. Unset until the session first qualifies as DCFC.
	dcfcPriced  bool
	dcfcRate    float64
	dcfcCur     string
	dcfcNetwork string
	// dcfcVerdict is network detection's verdict once made (auto-detect
	// on for the user), with the peak it was made at: a co-located site
	// is looked at again when the peak has grown, since a peak only one
	// network could deliver can settle it.
	dcfcVerdict       *DCFCVerdict
	dcfcPeakKW        float64
	dcfcRivianCharger bool // the verdict saw isRivianCharger

	// lastMeaningfulAt tracks the last frame where something real
	// happened: SoC moved up, charger power was above the idle floor,
	// or chargerState transitioned. The stale-session guard measures
	// its gap against this, not endAt, so a session can't be kept open
	// indefinitely by Rivian replaying sticky "charging_ready" frames.
	lastMeaningfulAt time.Time
}

// markMeaningful bumps lastMeaningfulAt if the incoming frame
// represents real activity: SoC ticked up, charger power was above
// the idle floor, or chargerState transitioned. Sticky-replay frames
// (same state, idle power, flat SoC) leave the timestamp alone so
// the stale-session guard can eventually trip.
func (c *liveCharge) markMeaningful(t time.Time, soc, kw float64, state string) {
	socUp := soc > c.endSoC
	active := kw >= chargingPowerFloorKW && kw <= maxLivePowerKW
	stateChanged := state != "" && state != c.finalState
	if socUp || active || stateChanged {
		c.lastMeaningfulAt = t
	}
}

// accumulateEnergy advances c.energyIntKWh by the trapezoidal area
// between the previous power sample and (t, kw). Invalid frames (kw
// out of physical range) are skipped without resetting the anchor so
// the next valid frame still integrates against the last good one.
// dt is clamped at maxIntegrationGap so a WS dropout can't inflate
// the integral.
func accumulateEnergy(c *liveCharge, t time.Time, kw float64) {
	if kw < 0 || kw > maxLivePowerKW {
		return
	}
	// Idle plugged-in frames report sub-floor parasitic draw (sentry,
	// thermal, cabin pre-cond) that isn't real charging. Skip
	// integration entirely while idle, and drop the anchor on every
	// idle frame so a subsequent active frame can't integrate across
	// the idle gap (which would convert an hour of 0.2 kW + a 5 kW
	// wake-up frame into ~2.6 kWh of phantom energy).
	if kw < chargingPowerFloorKW {
		c.lastPowerAt = time.Time{}
		c.lastPowerKW = 0
		return
	}
	if !c.lastPowerAt.IsZero() {
		dt := t.Sub(c.lastPowerAt)
		if dt > 0 {
			if dt > maxIntegrationGap {
				dt = maxIntegrationGap
			}
			c.energyIntKWh += dt.Hours() * (c.lastPowerKW + kw) / 2
			// Same gated dt feeds the active-charging-time accumulator,
			// so idle plugged-in stretches don't count toward duration.
			c.activeSeconds += dt.Seconds()
		}
	}
	c.lastPowerAt = t
	c.lastPowerKW = kw
}

// record is the central recorder entry point, called from every cache
// writer in monitor.go (REST seed, periodicRefresh, WS push, charging
// poller). It:
//
//  1. Writes a samples.Sample row with source="live" capturing the
//     merged State.
//  2. Detects drive-start (gear ∈ {R,N,D}) and charge-start
//     (chargerState → charging_*) transitions, opening a session
//     accumulator and upserting the opening stub.
//  3. On every update while a session is active, refreshes the stub's
//     end-state so a process crash still leaves a reasonable drive or
//     charge row in the table.
//  4. Detects drive-end (gear → P) and charge-end
//     (chargerState → charger_disconnected / charging_complete)
//     transitions, upserting the final row and clearing the
//     accumulator.
//
// All store writes are best-effort: errors are logged and swallowed
// so recording failures never break the live-state HTTP path.
func (m *StateMonitor) record(ctx context.Context, vehicleID string, prev, curr *State) {
	m.recordFrame(ctx, vehicleID, prev, curr, true /* lifecycle */)
}

// recordSampleOnly writes the sample row and updates the live-cache
// counters but does NOT run drive/charge lifecycle handlers. Used by
// the REST periodic refresh path: a stale REST snapshot can carry an
// outdated gear/chargerState (the gateway replays its last cached
// frame for cars in cellular dead-zones), and lifecycle decisions
// driven from there fragment a single real drive into many 2-3 min
// stub drives. Lifecycle should only be driven by the WS push and the
// initial REST seed; subsequent REST is pure cache fill.
func (m *StateMonitor) recordSampleOnly(ctx context.Context, vehicleID string, prev, curr *State) {
	m.recordFrame(ctx, vehicleID, prev, curr, false /* lifecycle */)
}

// minSampleSpacing is the floor between two persisted vehicle_state
// rows for one vehicle. Chosen to match the lower bound
// internal/recap/power.go applies when deriving power, so the writer
// never produces an interval the analysis has to discard.
//
// Well below the recorder's real cadence (1–5 s driving, ~30 s
// parked), so no genuine sample is lost — it only collapses the
// near-simultaneous writes that several feeds make of the same state.
const minSampleSpacing = 500 * time.Millisecond

// claimSampleSlot reports whether a sample stamped at is far enough
// from the last persisted one to be written, and records it if so.
// First sample for a vehicle always wins.
//
// Out-of-order frames (at before the last write) are also rejected:
// the samples table is read as a time series and a backwards row
// would produce a negative interval downstream.
func (m *StateMonitor) claimSampleSlot(vehicleID string, at time.Time) bool {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	if m.lastSampleAt == nil {
		m.lastSampleAt = make(map[string]time.Time)
	}
	last, ok := m.lastSampleAt[vehicleID]
	if ok && at.Sub(last) < minSampleSpacing {
		return false
	}
	m.lastSampleAt[vehicleID] = at
	return true
}

// forgetSampleSlot drops a vehicle's spacing cursor. Called on
// Unsubscribe so a re-subscribe isn't gated by a stale timestamp.
func (m *StateMonitor) forgetSampleSlot(vehicleID string) {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	delete(m.lastSampleAt, vehicleID)
}

func (m *StateMonitor) recordFrame(ctx context.Context, vehicleID string, prev, curr *State, lifecycle bool) {
	if curr == nil {
		return
	}

	// The vehicle reports its own usable pack capacity on every
	// vehicleState push (batteryCapacity field). Fold that into the
	// in-memory vehicleInfo cache so PackKWhFor prefers it over the
	// static InferPackKWh lookup table — the vehicle's self-report
	// is authoritative (and tracks the real pack, not a model-year
	// nameplate).
	if curr.BatteryCapacityKWh > 0 {
		m.observeBatteryCapacity(vehicleID, curr.BatteryCapacityKWh)
	}

	// Use a detached context with a generous timeout so recorder
	// writes can't block cache updates on a slow disk. context.Background
	// because the caller's ctx may be about to be cancelled (e.g. on
	// subscription shutdown) and we still want the last sample to land.
	//
	// 10s is well over what an idle Postgres needs (sub-millisecond)
	// but covers (a) Synology HDD-backed volumes during a checkpoint,
	// (b) the period right after migration 0007 when the new
	// vehicle_state partition indexes are warming, and (c) any
	// transient lock wait. Anything still slower than this points to
	// a real DB problem worth surfacing.
	wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx // reserved for future backpressure signals

	m.sessMu.Lock()
	sess := m.sessions[vehicleID]
	if sess == nil {
		// First contact for this vehicle in this process. If a
		// LiveStateStore is wired and this is the first attempt for
		// this vehicle (post-boot or post-Unsubscribe), try to
		// rehydrate the in-flight accumulator from Redis so a pod
		// restart or lease handoff mid-drive doesn't fragment the
		// session into a new drive row. The recorder's own
		// stale-session guards (liveDriveMaxGap / liveChargeMaxGap)
		// still run on the next lifecycle pass and will drop
		// rehydrated state that's clearly out of date.
		sess = m.maybeRehydrate(vehicleID, curr)
		m.sessions[vehicleID] = sess
	}

	var driveNum, chargeNum int64
	var snap LiveStateSnapshot
	saveSnapshot := false
	if lifecycle {
		// Physical-invariant guard: a car can't be driving and
		// charging at the same time. If the current frame says it's
		// doing one, any open accumulator for the OTHER must be
		// stale (Rivian's charger fields stick across unplug + drive
		// cycles, and the WS occasionally drops mid-session).
		// Force-close it BEFORE the lifecycle handlers so the new
		// gear/charge state opens a clean session instead of
		// extending the wrong one.
		//
		// The lifecycle sees lifecycleView(curr): a frame that says
		// both driving and charging is resolved by motion to one of
		// them, so a stale gear or stuck charger fields can't close and
		// reopen both sessions on every frame. The sample row keeps the
		// raw frame.
		lc := sess.lifecycleView(curr, m)
		sess.applyMutualExclusion(lc, m, wctx)

		// Handle session lifecycle so the sample row carries the
		// right drive_number / charge_number for this frame.
		driveNum = sess.handleDriveLifecycle(lc, prev, m, wctx)
		chargeNum = sess.handleChargeLifecycle(lc, prev, m, wctx)

		// Snapshot the post-lifecycle accumulator while we still
		// hold sessMu; the actual Redis write happens after unlock
		// so a slow round-trip doesn't serialise other vehicles'
		// frames behind it.
		if m.liveStateStore != nil {
			snap = sess.snapshot()
			saveSnapshot = true
		}
	} else {
		// Sample-only path: don't open or close any session, but DO
		// stamp the sample with whatever drive/charge is currently
		// open so the row stitches into the right session in the UI.
		if sess.drive != nil {
			driveNum = sess.drive.number
		}
		if sess.charge != nil {
			chargeNum = sess.charge.number
		}
	}
	m.sessMu.Unlock()

	if saveSnapshot {
		if m.deferringWrites() {
			m.catchup.setSnapshot(vehicleID, snap)
		} else {
			m.persistLiveState(vehicleID, snap)
		}
	}

	// Sample insert: one row per cache update, floored at
	// minSampleSpacing.
	//
	// "Naturally throttled" was the old assumption here, and it was
	// wrong. record() is reached from five independent writers — the
	// REST seed, the vehicleState WS push, the Parallax GNSS and
	// drive-dynamics subscribers, and the live-session applier — plus
	// recordSampleOnly from periodicRefresh. Each stamps its own At
	// (driveDynamicsSubscriber literally sets At = time.Now()), so
	// two feeds observing the SAME vehicle state milliseconds apart
	// produce two rows whose timestamps differ. The store's
	// ON CONFLICT (vehicle_id, at) key is exact, so it never fires
	// and both rows persist.
	//
	// Measured: 35,393 sub-0.5 s pairs in 30 days, 18,171 of them
	// carrying identical speed AND battery level. It began the week
	// of 2026-07-13, when the Parallax subscription was widened to
	// more topics, and lands mostly while the car is stationary —
	// parked frames (closures, locks, 12 V) change nothing the sample
	// records, but still trigger a write.
	//
	// Downstream this is not cosmetic: internal/recap/power.go models
	// power from dv/dt, and two rows 1 ms apart with different speeds
	// yield ~24,600 m/s^2. Flooring the writer at the same 0.5 s the
	// model floors its reader means the recorder now guarantees what
	// the analysis assumes.
	//
	// Dropping the loser of a 500 ms race is safe because every
	// caller passes the MERGED cache, not its own bare frame: each
	// writer copies m.cache[vehicleID], folds in its own fields,
	// stores it back, and hands that to record(). So a dropped
	// frame's data is already in the cache and lands in the next
	// accepted sample — the floor delays persistence by at most
	// 500 ms, it does not discard readings.
	//
	// The floor guards only the sample write. Lifecycle handling ran
	// above and must still see every frame — a gear or charger-state
	// transition arriving 100 ms after the previous sample is real
	// and must not be dropped. (dynamics.vehicle.gear is authoritative
	// for drive-open per docs/PARALLAX_MIGRATION.md.)
	//
	// This is a rate limiter, not the end state: row count still
	// scales with topic count, and the Parallax migration keeps
	// adding topics. See docs/ROADMAP.md, "Decouple sample
	// persistence from frame arrival", for the sampler design that
	// replaces it.
	if (m.samplesStore != nil || m.deferringWrites()) && m.claimSampleSlot(vehicleID, curr.At) {
		s := samples.Sample{
			VehicleID:       vehicleID,
			At:              curr.At,
			BatteryLevelPct: curr.BatteryLevelPct,
			RangeMi:         curr.DistanceToEmpty * kmToMi,
			OdometerMi:      curr.OdometerKm * kmToMi,
			Lat:             curr.Latitude,
			Lon:             curr.Longitude,
			SpeedMph:        curr.SpeedKph * kphToMi,
			ShiftState:      curr.Gear,
			DriveMode:       curr.DriveMode,
			ChargingState:   curr.ChargerState,
			ChargerStatus:   curr.ChargerStatus,
			PowerState:      curr.PowerState,
			ChargerPowerKW:  curr.ChargerPowerKW,
			ChargeLimitPct:  curr.ChargeTargetPct,
			InsideTempC:     curr.CabinTempC,
			OutsideTempC:    curr.OutsideTempC,
			DriveNumber:     driveNum,
			ChargeNumber:    chargeNum,
			Source:          "live",
		}
		// Only attach a fix timestamp when the gateway actually
		// reported one. The Rivian client zeros LocationFixAt
		// when the GNSS @defer slice is missing from the WS
		// frame; persisting that zero would later read back as
		// an epoch fix and the UI would render an ~17M-hour
		// "stale fix" badge.
		if !curr.LocationFixAt.IsZero() {
			fix := curr.LocationFixAt
			s.LocationFixAt = &fix
		}
		// Elevation lookup is best-effort: cache hits return in
		// microseconds, misses kick off an async tile fetch and
		// return ok=false so the column lands NULL. The next sample
		// on the now-warm tile gets a real altitude. We never block
		// the WS hot path on a network round trip.
		if m.elevation != nil && (curr.Latitude != 0 || curr.Longitude != 0) {
			if alt, ok := m.elevation.Lookup(curr.Latitude, curr.Longitude); ok {
				s.AltitudeM = &alt
			}
		}
		// Tire pressure: persist the minimum of the four corners
		// (in bar). Drop zeros — the gateway emits 0 when the
		// TPMS sensor hasn't been polled recently or after a
		// long park. Need at least one non-zero corner to record
		// anything; otherwise leave NULL.
		if minBar := minNonZero4(curr.TirePressureFLBar, curr.TirePressureFRBar, curr.TirePressureRLBar, curr.TirePressureRRBar); minBar > 0 {
			b := minBar
			s.TirePressureMinBar = &b
		}
		// Pack cell temperatures (from the Parallax battery_state topic,
		// carried across frames by mergeState). Record when we have a
		// reading; the avg gates all three since Rivian sends them
		// together — a zero avg means no frame yet, leave NULL.
		if curr.PackTempAvgC != 0 {
			avg, mx, mn := curr.PackTempAvgC, curr.PackTempMaxC, curr.PackTempMinC
			s.PackTempAvgC, s.PackTempMaxC, s.PackTempMinC = &avg, &mx, &mn
		}
		if m.deferringWrites() {
			// Replay catch-up: batched, see replay_catchup.go.
			m.catchup.addSample(vehicleID, s)
		} else if err := m.samplesStore.InsertBatch(wctx, []samples.Sample{s}); err != nil {
			// Warn (not Debug) — this is the only place a silent
			// vehicle_state write failure shows up, and a quiet
			// failure is exactly what produces "the drive list has
			// the drive but the map is empty". Anything that wants
			// to silence it can lift the level back via slog.
			m.logger.Warn("live sample insert failed", "vehicle", vehicleID, "err", err.Error())
		}
	}
}

// applyMutualExclusion enforces the physical invariant that a car
// can't be driving and charging simultaneously. Closes whichever
// accumulator contradicts the current frame. Must be called with
// m.sessMu held.
func (s *liveSessions) applyMutualExclusion(curr *State, m *StateMonitor, ctx context.Context) {
	driving := isDrivingGear(curr.Gear)
	chargingNow := isChargingCS(curr.ChargerState) && s.plugged(curr)
	// These closes finish a session like the normal close paths do: a
	// last upsert that retries a rejected row once, then the close hook
	// (charge notifications, drive weather) unless the row is a phantom
	// or another recorder's. They used to upsert and drop the session
	// without the hook.
	if driving && s.charge != nil {
		m.logger.Info("closing live charge: gear is driving",
			"vehicle", curr.VehicleID, "id", s.charge.id, "gear", curr.Gear)
		s.charge.finalState = "abandoned"
		s.charge.final = true
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		closedRow := m.liveChargeRow(curr.VehicleID, s.charge)
		phantom := !s.charge.endAt.After(s.charge.startedAt) && s.charge.endSoC == s.charge.startSoC
		rejected := s.charge.rejected
		s.charge = nil
		m.mu.Lock()
		delete(m.lastSession, curr.VehicleID)
		delete(m.chargeBond, curr.VehicleID)
		delete(m.lastSessionFor, curr.VehicleID)
		m.mu.Unlock()
		// A charge abandoned with next to nothing in it isn't worth a
		// "charge finished" notification.
		if !phantom && !rejected && closedRow.EnergyAddedKWh >= mutexChargeHookMinKWh {
			m.fireChargeCloseHook(closedRow)
		}
	}
	if chargingNow && s.drive != nil {
		m.logger.Info("closing live drive: car is charging",
			"vehicle", curr.VehicleID, "id", s.drive.id, "charger_state", curr.ChargerState)
		s.drive.final = true
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		closedRow := m.liveDriveRow(curr.VehicleID, s.drive)
		phantom := !s.drive.endAt.After(s.drive.startedAt) && s.drive.endSoC == s.drive.startSoC && s.drive.endOdoMi == s.drive.startOdoMi
		rejected := s.drive.rejected
		s.drive = nil
		// A drive that ran straight into charging only gets the hook
		// (weather) if it was a real drive.
		if !phantom && !rejected && (closedRow.EndedAt.Sub(closedRow.StartedAt) >= mutexDriveHookMinDur ||
			closedRow.DistanceMi >= mutexDriveHookMinMi) {
			m.fireDriveCloseHook(closedRow)
		}
	}
}

// Thresholds for the close hooks of sessions closed by
// applyMutualExclusion: a forced close is often a fragment (a stale
// gear, stuck charger fields), and its hooks notify the user or fetch
// weather.
const (
	mutexChargeHookMinKWh = 1.0
	mutexDriveHookMinDur  = 60 * time.Second
	mutexDriveHookMinMi   = 0.1
)

// hookOnceMax bounds the fired-hook set; past it, entries older than
// hookOnceTTL are dropped (a session closes once, and a duplicate close
// comes seconds or minutes later, not a day).
const (
	hookOnceMax = 1024
	hookOnceTTL = 24 * time.Hour
)

// hookOnce reports whether the close hook for session id (kind "d" or
// "c") may fire, recording it: every close path fires a session's hook
// at most once, however many times it's closed (a flap closed the same
// charge id hundreds of times).
func (m *StateMonitor) hookOnce(kind, id string) bool {
	now := time.Now()
	key := kind + "|" + id
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hooksFired == nil {
		m.hooksFired = map[string]time.Time{}
	}
	if _, done := m.hooksFired[key]; done {
		return false
	}
	if len(m.hooksFired) >= hookOnceMax {
		for k, at := range m.hooksFired {
			if now.Sub(at) > hookOnceTTL {
				delete(m.hooksFired, k)
			}
		}
		if len(m.hooksFired) >= hookOnceMax {
			m.hooksFired = map[string]time.Time{}
		}
	}
	m.hooksFired[key] = now
	return true
}

// fireChargeCloseHook runs the charge close hook for row, once per
// session, unless hooks are off (none set, or replay catch-up).
func (m *StateMonitor) fireChargeCloseHook(row charges.Charge) {
	if m.chargeCloseHook == nil || m.quietHooks.Load() || !m.hookOnce("c", row.ID) {
		return
	}
	go m.runChargeCloseHook(row)
}

// fireDriveCloseHook is fireChargeCloseHook for drives.
func (m *StateMonitor) fireDriveCloseHook(row drives.Drive) {
	if m.driveCloseHook == nil || m.quietHooks.Load() || !m.hookOnce("d", row.ID) {
		return
	}
	go m.runDriveCloseHook(row)
}

// switchMovedMi: the odometer moving this far since the open charge
// began means the car is driving, whatever the charger fields say.
const switchMovedMi = 0.05

// lifecycleView is the frame the session lifecycle acts on. A frame that
// says both driving (gear D/R/N) and charging (plugged, charging_active)
// is a contradiction: Rivian's charger fields stick after an unplug, and
// a replayed gear can read D while the car charges (the 2026-10-10 flap,
// where the recorder then closed and reopened both sessions on every
// frame). Motion settles it: speed over driveCloseSpeedMph, the odometer
// moving switchMovedMi since the open charge began, or a fresh Parallax
// driving gear mean driving, and the lifecycle sees the charger fields
// as unknown; otherwise the car is charging and it sees gear P. Frames
// that don't contradict themselves pass through unchanged (an unplug
// followed by D still closes the charge and opens the drive at once).
//
// Must be called with m.sessMu held.
func (s *liveSessions) lifecycleView(curr *State, m *StateMonitor) *State {
	if !isDrivingGear(curr.Gear) || !(isChargingCS(curr.ChargerState) && s.plugged(curr)) {
		return curr
	}
	moving := curr.SpeedKph*kphToMi > driveCloseSpeedMph
	if c := s.charge; c != nil && c.startOdoMi > 0 && curr.OdometerKm > 0 &&
		curr.OdometerKm*kmToMi-c.startOdoMi >= switchMovedMi {
		moving = true
	}
	if g, at := m.lastParallaxGear(curr.VehicleID); isDrivingGear(g) && !at.IsZero() && time.Since(at) < 30*time.Second {
		moving = true
	}
	view := *curr
	if moving {
		view.ChargerState, view.ChargerStatus, view.ChargerPowerKW = "", "", 0
	} else {
		view.Gear = "P"
	}
	return &view
}

// handleDriveLifecycle manages the drive accumulator across a single
// state transition. Returns the drive_number to stamp on this frame's
// sample row (0 if not currently driving).
//
// Must be called with m.sessMu held.
func (s *liveSessions) handleDriveLifecycle(curr, prev *State, m *StateMonitor, ctx context.Context) int64 {
	_ = prev // reserved for future transition-aware logic (e.g. only upserting on real changes).
	driving := isDrivingGear(curr.Gear)

	// Stale-session guard (drive). If the accumulator hasn't seen a
	// frame in a long time, the WS likely dropped and reconnected on
	// a fresh drive — close the old in-memory session so we don't
	// straddle two real drives.
	//
	// Gate on a strictly-forward clock delta. State.At is now
	// wall-clock so this should always be ≥0, but a defensive
	// gap > 0 guard keeps us safe against future regressions
	// (a regressed clock used to fragment trips into 3-min
	// stubs whenever a frame carried a stale GNSS timestamp)
	// and any out-of-order frame ordering bugs.
	if driving && s.drive != nil {
		gap := curr.At.Sub(s.drive.endAt)
		if gap > liveDriveMaxGap {
			m.logger.Info("closing stale live drive",
				"vehicle", curr.VehicleID,
				"id", s.drive.id,
				"gap", gap.Round(time.Second))
			m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
			s.drive = nil
		}
	}

	// Open new drive on transition P/"" → D/R/N — unless this is a
	// reopen right after a debounced close, in which case resume the
	// previous accumulator: same row id, stats and path continue. This
	// heals any blip that slips past the close debounce and folds
	// parking-lot D-P-D shuffles into one drive instead of stub rows.
	if driving && s.drive == nil {
		if lc := s.lastClosed; lc != nil {
			since := curr.At.Sub(s.lastClosedAt)
			odoNow := curr.OdometerKm * kmToMi
			if since >= 0 && since <= driveReopenWindow &&
				(odoNow <= 0 || odoNow-lc.endOdoMi <= driveReopenMaxMi) {
				m.logger.Info("resuming just-closed live drive",
					"vehicle", curr.VehicleID, "id", lc.id,
					"gap", since.Round(time.Second).String())
				lc.pendingCloseAt = time.Time{}
				s.drive = lc
				s.lastClosed = nil
				// Fall through to the ongoing-drive block below, which
				// folds this frame into the resumed accumulator and
				// re-upserts the row.
			}
		}
	}
	if driving && s.drive == nil {
		s.driveCounter++
		odoMi := curr.OdometerKm * kmToMi
		s.drive = &liveDrive{
			id:         liveSessionID(curr.VehicleID, "d", curr.At),
			startedAt:  curr.At,
			number:     s.driveCounter,
			startSoC:   curr.BatteryLevelPct,
			startOdoMi: odoMi,
			startLat:   curr.Latitude,
			startLon:   curr.Longitude,
			endAt:      curr.At,
			endSoC:     curr.BatteryLevelPct,
			endOdoMi:   odoMi,
			endLat:     curr.Latitude,
			endLon:     curr.Longitude,
		}
		if curr.Latitude != 0 || curr.Longitude != 0 {
			s.drive.path = append(s.drive.path, [2]float64{curr.Latitude, curr.Longitude})
			s.drive.lastFixAt = curr.At
		}
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		return s.drive.number
	}

	// Drive ongoing: update running end state and speed aggregates.
	if driving && s.drive != nil {
		mph := curr.SpeedKph * kphToMi
		if mph > s.drive.maxSpeed {
			s.drive.maxSpeed = mph
		}
		if mph > 0 {
			s.drive.sumSpeed += mph
			s.drive.speedN++
		}
		// endAt is monotonic-forward only. A regressed clock from
		// any source must not pull the row's "ended_at" backwards
		// — that path produced negative-duration rows in the past.
		if curr.At.After(s.drive.endAt) {
			s.drive.endAt = curr.At
		}
		s.drive.endSoC = curr.BatteryLevelPct
		if odoMi := curr.OdometerKm * kmToMi; odoMi > 0 {
			s.drive.endOdoMi = odoMi
		}
		// A driving frame cancels any pending (uncorroborated) close —
		// the P that started it was a phantom blip.
		s.drive.pendingCloseAt = time.Time{}
		if curr.Latitude != 0 || curr.Longitude != 0 {
			// Fix-timestamp monotonicity: Parallax gnss is the GPS
			// backbone and vehicleState fills detail between its ticks —
			// but a stale vehicleState replay (the gateway re-sends an
			// old fix) must not drag the path/end position backwards
			// after a newer Parallax fix has landed.
			fresh := curr.LocationFixAt.IsZero() || s.drive.lastFixTS.IsZero() ||
				curr.LocationFixAt.After(s.drive.lastFixTS)
			if fresh {
				s.drive.endLat = curr.Latitude
				s.drive.endLon = curr.Longitude
				m.appendDrivePath(ctx, curr.VehicleID, s.drive, curr.Latitude, curr.Longitude, curr.At)
				if !curr.LocationFixAt.IsZero() {
					s.drive.lastFixTS = curr.LocationFixAt
				}
			}
		}
		// Periodically re-upsert so a crash preserves the latest
		// state. Cheap: single-row upsert against a sub-1k-row table.
		// (Batched during replay catch-up, see replay_catchup.go.)
		m.touchLiveDrive(ctx, curr.VehicleID, s.drive)
		return s.drive.number
	}

	// Close drive on transition D/R/N → P — debounced. vehicleState
	// emits phantom P blips mid-drive (observed: P at 5 mph for 2-9s,
	// then D again) and closing on a single frame split real trips.
	// Commit the close only when the stop is corroborated: speed
	// at/near zero, a fresh Parallax gear frame agreeing on P, or P
	// persisting past driveCloseDebounce (covers the blackout case
	// where the parking frame carries a stale speed). A fresh Parallax
	// frame still saying R/N/D vetoes the fast paths — the veto decays
	// with frame freshness, so it can't hold the drive open forever.
	if !driving && s.drive != nil {
		if s.drive.pendingCloseAt.IsZero() {
			s.drive.pendingCloseAt = curr.At
		}
		mph := curr.SpeedKph * kphToMi
		pxGear, pxAt := m.lastParallaxGear(curr.VehicleID)
		pxFresh := !pxAt.IsZero() && time.Since(pxAt) < 30*time.Second
		pxParked := pxFresh && pxGear == "P"
		pxDriving := pxFresh && isDrivingGear(pxGear)
		confirmed := pxParked ||
			(!pxDriving && (mph <= driveCloseSpeedMph ||
				curr.At.Sub(s.drive.pendingCloseAt) >= driveCloseDebounce))
		if !confirmed {
			// Not corroborated yet: keep the drive open and stamp this
			// sample with the still-open drive number. Either a driving
			// frame arrives (blip → pendingCloseAt clears above) or the
			// debounce expires on a later P frame.
			return s.drive.number
		}
		// The parking frame carries the drive's true final odometer.
		// The ongoing-drive block above only advances end state while
		// the gear reads driving, so if a telemetry gap swallowed the
		// last stretch before the car stopped (Rivian's stream can go
		// quiet for minutes, then resume already in P), the last
		// *driving* frame we saw undercounts distance and ends the trip
		// early. Fold the parking frame's odometer in here. Advance
		// ended_at / end position only when the odometer actually grew
		// across the gap — that's the signal the car was driving, not
		// idling; without it, post-park idle before the first P frame
		// would be miscounted as drive time.
		if odoMi := curr.OdometerKm * kmToMi; odoMi > s.drive.endOdoMi {
			s.drive.endOdoMi = odoMi
			s.drive.endSoC = curr.BatteryLevelPct
			if curr.At.After(s.drive.endAt) {
				s.drive.endAt = curr.At
			}
			if curr.Latitude != 0 || curr.Longitude != 0 {
				s.drive.endLat = curr.Latitude
				s.drive.endLon = curr.Longitude
			}
		}
		// Map endpoint splice: extend the polyline to the parking spot.
		// After a telemetry blackout the odometer fold above fixes the
		// numbers but the path still ended at the last live fix — this
		// route-fills from there to the parking frame's location so the
		// rendered route reaches the true endpoint.
		m.appendDrivePath(ctx, curr.VehicleID, s.drive, curr.Latitude, curr.Longitude, curr.At)
		s.drive.final = true // a rejected drive retries once more at close
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		// Snapshot the row before the close hook fires so any async
		// hook sees the persisted shape, not a half-mutated
		// accumulator. Hooks run with their own ctx so a slow
		// network call (weather fetch) can't block the recorder.
		closedRow := m.liveDriveRow(curr.VehicleID, s.drive)
		// Phantom drives (zero duration + zero deltas) were skipped by
		// upsertLiveDrive and never reached the store — don't run the
		// post-close hook for them either.
		phantom := !s.drive.endAt.After(s.drive.startedAt) && s.drive.endSoC == s.drive.startSoC && s.drive.endOdoMi == s.drive.startOdoMi
		// Still rejected after the close retry: another recorder owns
		// this drive, so its hooks are that recorder's to fire.
		rejected := s.drive.rejected
		n := s.drive.number
		// Retain for merge-on-reopen: a driving frame arriving within
		// driveReopenWindow (odometer barely moved) resumes this row.
		s.lastClosed = s.drive
		s.lastClosedAt = curr.At
		s.drive = nil
		// Not for drives replayed during catch-up: a first connect
		// backfills weeks of them, each fetching weather.
		if !phantom && !rejected {
			m.fireDriveCloseHook(closedRow)
		}
		return n
	}
	return 0
}

// runChargeCloseHook invokes the configured ChargeCloseHook with a
// detached context bounded by hookTimeout. Best-effort: panics and
// errors are caught/logged so a buggy hook can't crash the recorder.
// appendDrivePath appends a GPS fix to the drive's path, deduping
// consecutive identical points (Rivian replays the last cached fix for
// several frames around park events) and route-filling across long gaps:
// if the last appended fix is far enough behind in time AND the straight-
// line jump is long enough to look wrong on a map, ask the routing engine
// for a road-snapped shape and splice it in. Shared by the ongoing-drive
// frame path and the close-time endpoint splice. Must be called with
// m.sessMu held (it mutates the accumulator).
func (m *StateMonitor) appendDrivePath(ctx context.Context, vehicleID string, d *liveDrive, lat, lon float64, at time.Time) {
	if lat == 0 && lon == 0 {
		return
	}
	n := len(d.path)
	if n > 0 && d.path[n-1][0] == lat && d.path[n-1][1] == lon {
		return
	}
	if n > 0 && m.routeFiller != nil && !d.lastFixAt.IsZero() {
		last := d.path[n-1]
		gap := at.Sub(d.lastFixAt)
		dist := haversineMeters(last[0], last[1], lat, lon)
		if gap > 10*time.Second && dist > 100 && dist < 50_000 {
			fillCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			startedAt := time.Now()
			shape, err := m.routeFiller.RouteShape(fillCtx, last, [2]float64{lat, lon})
			elapsed := time.Since(startedAt)
			cancel()
			if err != nil {
				// Warn, not Debug: a failed fill degrades the rendered
				// route to a straight line across the gap. elapsed_ms
				// distinguishes a timeout (near the 750ms budget) from a
				// fast upstream error.
				m.logger.Warn("route-fill failed",
					"vehicle", vehicleID,
					"gap", gap.Round(time.Second),
					"dist_m", int(dist),
					"elapsed_ms", elapsed.Milliseconds(),
					"err", err.Error())
			} else if len(shape) > 2 {
				// Drop the first vertex (== last) and the last (== the
				// point we're about to append).
				d.path = append(d.path, shape[1:len(shape)-1]...)
			}
		}
	}
	d.path = append(d.path, [2]float64{lat, lon})
	d.lastFixAt = at
}

func (m *StateMonitor) runChargeCloseHook(row charges.Charge) {
	const hookTimeout = 30 * time.Second
	defer func() {
		if r := recover(); r != nil {
			m.logger.Warn("charge close hook panicked",
				"vehicle", row.VehicleID, "charge_id", row.ID, "panic", r)
		}
	}()
	hctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	m.chargeCloseHook(hctx, row)
}

// runDriveCloseHook invokes the configured DriveCloseHook with a
// detached context bounded by hookTimeout. Best-effort: panics and
// errors are caught/logged so a buggy hook can't crash the recorder.
func (m *StateMonitor) runDriveCloseHook(row drives.Drive) {
	const hookTimeout = 30 * time.Second
	defer func() {
		if r := recover(); r != nil {
			m.logger.Warn("drive close hook panicked",
				"vehicle", row.VehicleID, "drive_id", row.ID, "panic", r)
		}
	}()
	hctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	m.driveCloseHook(hctx, row)
}

// handleChargeLifecycle is the charge-session analogue to
// handleDriveLifecycle. Must be called with m.sessMu held.
func (s *liveSessions) handleChargeLifecycle(curr, prev *State, m *StateMonitor, ctx context.Context) int64 {
	_ = prev // reserved, see handleDriveLifecycle.
	// Gate the session predicate on BOTH the negotiating state AND the
	// physical plug indicator. Rivian's charger_state field sticks at
	// 'charging_ready' / 'charging_active' for hours after a cable is
	// pulled — without the plug check a spurious post-unplug frame
	// opens a phantom session that then absorbs stale 25 kWh from
	// applyLiveSession's cache and runs for hours with a DROPPING SoC.
	// See v0.3.48 for the matching frontend gate.
	charging := isChargingCS(curr.ChargerState) && s.plugged(curr)
	// Some vehicles report charging_ready while driving and parked
	// unplugged (R2). Where that's so, charging_ready doesn't open a
	// charge; charging_active does. An open charge isn't affected.
	if charging && s.charge == nil && curr.ChargerPowerKW <= 0 &&
		strings.EqualFold(strings.TrimSpace(curr.ChargerState), "charging_ready") && m.readyUnreliable(curr.VehicleID) {
		charging = false
	}

	// Stale-session guard. Force-close if too long has passed since
	// anything meaningful happened (SoC went up, power was above the
	// idle floor, or chargerState transitioned), OR SoC has dropped
	// since the last frame. The gap is measured against
	// lastMeaningfulAt — NOT endAt — so a session can't be kept open
	// by Rivian replaying sticky 'charging_ready' frames with no real
	// activity behind them.
	if charging && s.charge != nil {
		ref := s.charge.lastMeaningfulAt
		if ref.IsZero() {
			ref = s.charge.startedAt
		}
		gap := curr.At.Sub(ref)
		socDrop := s.charge.endSoC - curr.BatteryLevelPct
		if gap > liveChargeMaxGap || socDrop > liveChargeMaxSoCDropPct {
			m.logger.Info("closing stale live charge",
				"vehicle", curr.VehicleID,
				"id", s.charge.id,
				"gap", gap.Round(time.Second),
				"soc_drop", socDrop)
			s.charge.finalState = "abandoned"
			m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
			s.charge = nil
			m.mu.Lock()
			delete(m.lastSession, curr.VehicleID)
			delete(m.chargeBond, curr.VehicleID)
			delete(m.lastSessionFor, curr.VehicleID)
			m.mu.Unlock()
		}
	}

	// Open new charge.
	if charging && s.charge == nil {
		// Resurrect-on-restart: if the charges store already has an
		// open live session for this vehicle (process was killed
		// mid-charge), reattach to it instead of minting a new ID.
		// Otherwise every restart orphans the previous row and opens
		// a duplicate `live_<vid>_c_<unix>`.
		if resumed := m.resumeOpenCharge(ctx, curr); resumed != nil {
			s.charge = resumed
			s.chargeCounter++
			s.charge.number = s.chargeCounter
			// Update end-state to the current frame so the resurrected
			// row advances forward on the next upsert. Monotonic-forward
			// only -- don't let the resumed row's persisted endAt be
			// regressed by a stale-clock frame.
			if curr.At.After(s.charge.endAt) {
				s.charge.endAt = curr.At
			}
			s.charge.markMeaningful(curr.At, curr.BatteryLevelPct, curr.ChargerPowerKW, curr.ChargerState)
			s.charge.endSoC = curr.BatteryLevelPct
			s.charge.finalState = curr.ChargerState
			if curr.ChargerPowerKW > s.charge.maxPower && curr.ChargerPowerKW <= maxLivePowerKW {
				s.charge.maxPower = curr.ChargerPowerKW
			}
			accumulateEnergy(s.charge, curr.At, curr.ChargerPowerKW)
			m.bondCharge(curr.VehicleID, s.charge.id)
			m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
			m.closeStaleOpenCharges(ctx, curr.VehicleID, s.charge.id)
			return s.charge.number
		}
		s.chargeCounter++
		s.charge = &liveCharge{
			id:               liveSessionID(curr.VehicleID, "c", curr.At),
			startedAt:        curr.At,
			number:           s.chargeCounter,
			startSoC:         curr.BatteryLevelPct,
			startOdoMi:       curr.OdometerKm * kmToMi,
			lat:              curr.Latitude,
			lon:              curr.Longitude,
			endAt:            curr.At,
			endSoC:           curr.BatteryLevelPct,
			finalState:       curr.ChargerState,
			lastMeaningfulAt: curr.At,
		}
		if curr.ChargerPowerKW > 0 && curr.ChargerPowerKW <= maxLivePowerKW {
			s.charge.maxPower = curr.ChargerPowerKW
		}
		accumulateEnergy(s.charge, curr.At, curr.ChargerPowerKW)
		m.bondCharge(curr.VehicleID, s.charge.id)
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		m.closeStaleOpenCharges(ctx, curr.VehicleID, s.charge.id)
		return s.charge.number
	}

	// Charge ongoing: update running aggregates.
	if charging && s.charge != nil {
		if curr.ChargerPowerKW > s.charge.maxPower && curr.ChargerPowerKW <= maxLivePowerKW {
			s.charge.maxPower = curr.ChargerPowerKW
		}
		s.charge.markMeaningful(curr.At, curr.BatteryLevelPct, curr.ChargerPowerKW, curr.ChargerState)
		accumulateEnergy(s.charge, curr.At, curr.ChargerPowerKW)
		// endAt is monotonic-forward only — see the drive-lifecycle
		// branch above for the regressed-clock class this guards.
		if curr.At.After(s.charge.endAt) {
			s.charge.endAt = curr.At
		}
		s.charge.endSoC = curr.BatteryLevelPct
		s.charge.finalState = curr.ChargerState
		m.touchLiveCharge(ctx, curr.VehicleID, s.charge)
		return s.charge.number
	}

	// Close charge on terminal state.
	if !charging && s.charge != nil {
		// Pull the latest frame's SoC / timestamp / peak power into
		// the row before closing. Without this, a WS that goes silent
		// mid-session and reconnects only after the car has finished
		// charging (so the very first post-gap frame is already
		// charging_complete) closes the row with the pre-gap endSoC,
		// producing rows where energy_added is correct but endSoC
		// reflects the SoC from when the WS died, not the actual
		// final state. Mirrors the "charge ongoing" branch above.
		// endAt is monotonic-forward to defend against any
		// regressed-clock close frame.
		if curr.At.After(s.charge.endAt) {
			s.charge.endAt = curr.At
		}
		if curr.BatteryLevelPct > s.charge.endSoC {
			s.charge.endSoC = curr.BatteryLevelPct
		}
		if curr.ChargerPowerKW > s.charge.maxPower && curr.ChargerPowerKW <= maxLivePowerKW {
			s.charge.maxPower = curr.ChargerPowerKW
		}
		accumulateEnergy(s.charge, curr.At, curr.ChargerPowerKW)
		s.charge.finalState = curr.ChargerState
		s.charge.final = true // a rejected charge retries once more at close
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		// Snapshot the row before the hook fires (same pattern as the
		// drive-close path) and decide whether to skip the hook for
		// phantom rows that upsertLiveCharge already filtered out.
		closedRow := m.liveChargeRow(curr.VehicleID, s.charge)
		phantom := !s.charge.endAt.After(s.charge.startedAt) && s.charge.endSoC == s.charge.startSoC
		if !phantom {
			m.mu.RLock()
			var sess *LiveSession
			if m.lastSessionFor[curr.VehicleID] == s.charge.id {
				sess = m.lastSession[curr.VehicleID]
			}
			m.mu.RUnlock()
			logChargeClose(m.logger, curr.VehicleID, closedRow, sess, s.charge)
		}
		// Still rejected after the close retry: no duplicate push.
		rejected := s.charge.rejected
		n := s.charge.number
		s.charge = nil
		// Drop the cached LiveSession so its fields (energy, active,
		// start_time, price) can't leak into the next session. The
		// applyLiveSession merger intentionally preserves non-zero
		// values across pushes to handle interleaved ChargingSession
		// and Parallax frames within one session — without this reset
		// those values stick across sessions too, and a spurious
		// "charging_ready" minutes later would inherit the prior
		// session's 25 kWh total and Active=true flag.
		m.mu.Lock()
		delete(m.lastSession, curr.VehicleID)
		delete(m.chargeBond, curr.VehicleID)
		delete(m.lastSessionFor, curr.VehicleID)
		m.mu.Unlock()
		if !phantom && !rejected {
			m.fireChargeCloseHook(closedRow)
		}
		return n
	}
	return 0
}

// sessionPredatesCharge reports evidence that a bonded session began well
// before the charge it's bonded to: it has run longer than the charge
// by more than 15 min and a quarter of the charge's age, or its start
// time is over 15 min before the charge's. Rivian's REST and replayed
// snapshots can describe the previous session; its price and charger
// flag must not land on this one. No evidence (no elapsed time, no
// parsable start) is not staleness.
func sessionPredatesCharge(sess *LiveSession, c *liveCharge) bool {
	if sess == nil || c == nil {
		return false
	}
	age := c.endAt.Sub(c.startedAt)
	if sess.TimeElapsedSeconds > 0 && time.Duration(sess.TimeElapsedSeconds)*time.Second > age+15*time.Minute+age/4 {
		return true
	}
	// The same slack for the start time: Rivolt can open a charge late
	// (a WS gap at plug-in, or an R2's charge opening only once power
	// flows), and a 3 h session opened 20 min late is still its own.
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(sess.StartTime)); err == nil &&
		t.Before(c.startedAt.Add(-(15*time.Minute + age/4))) {
		return true
	}
	return false
}

// sessionStartDelta is the bonded session's StartTime minus the
// charge's start (negative: the session began first); ok=false with no
// parsable StartTime. Logged at close to measure sessionPredatesCharge.
func sessionStartDelta(sess *LiveSession, c *liveCharge) (time.Duration, bool) {
	if sess == nil || c == nil {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(sess.StartTime))
	if err != nil {
		return 0, false
	}
	return t.Sub(c.startedAt), true
}

// NetworkRivianAdventure is the display name the ledger and the charge
// map use for the Rivian Adventure Network.
const NetworkRivianAdventure = "Rivian Adventure Network"

// logChargeClose logs one line per closed charge: "dcfc session closed"
// (whether Rivian reported isRivianCharger, so the network rule's
// coverage can be measured) or "ac session closed". Both carry the
// bonded session's start_delta_s / elapsed_s and the
// sessionPredatesCharge verdict, so how often the freshness rule fires
// on real charges can be measured.
func logChargeClose(logger *slog.Logger, vehicleID string, row charges.Charge, sess *LiveSession, c *liveCharge) {
	if logger == nil {
		return
	}
	attrs := []any{"vehicle", vehicleID, "kwh", row.EnergyAddedKWh, "cost_source", row.CostSource, "network", row.Network,
		"session_bonded", sess != nil, "session_stale", sessionPredatesCharge(sess, c)}
	if d, ok := sessionStartDelta(sess, c); ok {
		attrs = append(attrs, "start_delta_s", int64(d.Seconds()))
	}
	if sess != nil && sess.TimeElapsedSeconds > 0 {
		attrs = append(attrs, "elapsed_s", sess.TimeElapsedSeconds, "age_s", int64(c.endAt.Sub(c.startedAt).Seconds()))
	}
	if IsDCFCSession(row.MaxPowerKW, row.EnergyAddedKWh, row.ActiveSeconds, row.EndedAt.Sub(row.StartedAt)) {
		seen, rivian := row.RivianCharger != nil, row.RivianCharger != nil && *row.RivianCharger
		logger.Info("dcfc session closed", append(attrs, "rivian_charger_seen", seen, "rivian_charger", rivian)...)
		return
	}
	logger.Info("ac session closed", attrs...)
}

// maybeRehydrate is the lazy-init path for sessions[vehicleID]. The
// first time we see a vehicle in this process (or after Unsubscribe
// cleared the rehydrated flag), it consults liveStateStore for a
// snapshot left behind by the previous lease owner. Stale snapshots
// (older than liveDriveMaxGap relative to the current frame) are
// dropped so a long-since-finished drive doesn't reattach.
//
// Caller holds sessMu.
func (m *StateMonitor) maybeRehydrate(vehicleID string, curr *State) *liveSessions {
	if m.liveStateStore == nil {
		return &liveSessions{}
	}
	if m.rehydrated[vehicleID] {
		return &liveSessions{}
	}
	m.rehydrated[vehicleID] = true

	loadCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, ok, err := m.liveStateStore.Load(loadCtx, vehicleID)
	if err != nil {
		m.logger.Debug("livestate load failed", "vehicle", vehicleID, "err", err.Error())
		return &liveSessions{}
	}
	if !ok {
		return &liveSessions{}
	}

	// Drop snapshots whose drive/charge endAt is clearly stale. The
	// stale-session guards in handleDriveLifecycle / handleChargeLifecycle
	// would also catch this on the next frame, but throwing away the
	// expired half here keeps logs cleaner and prevents an old
	// driveCounter from leaking into a brand-new session.
	now := curr.At
	if snap.Drive != nil && now.Sub(snap.Drive.EndAt) > liveDriveMaxGap {
		snap.Drive = nil
	}
	if snap.Charge != nil && now.Sub(snap.Charge.EndAt) > liveChargeMaxGap {
		snap.Charge = nil
	}

	sess := liveSessionsFromSnapshot(snap)
	if sess.drive != nil || sess.charge != nil {
		m.logger.Info("live state rehydrated",
			"vehicle", vehicleID,
			"drive_open", sess.drive != nil,
			"charge_open", sess.charge != nil,
			"drive_counter", sess.driveCounter,
			"charge_counter", sess.chargeCounter)
	}
	return sess
}

// persistLiveState saves the current accumulator snapshot to the
// LiveStateStore. Best-effort: storage failures are logged at debug
// and never propagated. Should be called after every WS-driven
// lifecycle pass so a pod restart between frames can rehydrate from
// the latest snapshot. Caller MUST NOT hold sessMu — Save can issue
// a network round-trip and we don't want to serialise frames across
// vehicles behind it.
func (m *StateMonitor) persistLiveState(vehicleID string, snap LiveStateSnapshot) {
	if m.liveStateStore == nil {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.liveStateStore.Save(saveCtx, vehicleID, snap, LiveStateTTL); err != nil {
		m.logger.Debug("livestate save failed", "vehicle", vehicleID, "err", err.Error())
	}
}

// liveDriveRow builds a drives.Drive value from the in-memory
// accumulator. Used by upsertLiveDrive on every WS frame to refresh
// the row, and by the D→P close path to hand a stable copy to any
// post-close hook (weather fetch, etc.) without exposing liveDrive
// across package boundaries.
func (m *StateMonitor) liveDriveRow(vehicleID string, d *liveDrive) drives.Drive {
	avg := 0.0
	if d.speedN > 0 {
		avg = d.sumSpeed / float64(d.speedN)
	}
	distance := d.endOdoMi - d.startOdoMi
	if distance < 0 {
		distance = 0
	}
	// Pack-side energy consumed, derived from SoC delta × usable pack
	// capacity. Same fallback the live /api/drive-live snapshot uses.
	var energy float64
	if socUsed := d.startSoC - d.endSoC; socUsed > 0 {
		if pack := m.PackKWhFor(vehicleID); pack > 0 {
			energy = socUsed / 100.0 * pack
		}
	}
	row := drives.Drive{
		ID:              d.id,
		VehicleID:       vehicleID,
		StartedAt:       d.startedAt,
		EndedAt:         d.endAt,
		StartSoCPct:     d.startSoC,
		EndSoCPct:       d.endSoC,
		StartOdometerMi: d.startOdoMi,
		EndOdometerMi:   d.endOdoMi,
		DistanceMi:      distance,
		StartLat:        d.startLat,
		StartLon:        d.startLon,
		EndLat:          d.endLat,
		EndLon:          d.endLon,
		MaxSpeedMph:     d.maxSpeed,
		AvgSpeedMph:     avg,
		EnergyUsedKWh:   energy,
		Source:          "live",
		RoutePolyline:   encodePolyline(d.path),
	}
	trimDrive(&row, d.trim, m.PackKWhFor(vehicleID))
	return row
}

func (m *StateMonitor) upsertLiveDrive(ctx context.Context, vehicleID string, d *liveDrive) {
	if d == nil {
		return
	}
	if m.catchup != nil {
		// This write supersedes any buffered in-session row.
		m.catchup.forgetDrive(vehicleID, d.id)
	}
	if m.drivesStore == nil {
		return
	}
	// Phantom-drive guard, mirroring upsertLiveCharge. A drive with zero
	// wall-clock duration AND zero SoC / odometer delta is a single sticky
	// gear frame that opened and closed in the same tick — not a real drive.
	// Real drives advance endAt within seconds of opening, so the periodic
	// upsert path picks up the row on the next frame regardless.
	if !d.endAt.After(d.startedAt) && d.endSoC == d.startSoC && d.endOdoMi == d.startOdoMi {
		return
	}
	if d.rejected && !m.prepareRejectedDrive(ctx, vehicleID, d) {
		return
	}
	row := m.liveDriveRow(vehicleID, d)
	if err := m.drivesStore.Upsert(ctx, row); err != nil {
		if sessionOverlapRejected(err) {
			if !d.rejected {
				m.logger.Warn("live drive rejected: overlaps another live drive for this vehicle (duplicate recorder?); retrying with a trimmed start",
					"vehicle", vehicleID, "id", d.id, "err", err.Error())
			}
			d.rejected, d.rejectedAt = true, time.Now()
			return
		}
		m.logger.Debug("live drive upsert failed", "vehicle", vehicleID, "id", d.id, "err", err.Error())
		return
	}
	if d.rejected {
		d.rejected = false
		m.logger.Info("rejected live drive written after the overlapping one", "vehicle", vehicleID, "id", d.id, "from", row.StartedAt)
	}
}

func (m *StateMonitor) upsertLiveCharge(ctx context.Context, vehicleID string, c *liveCharge) {
	if c == nil {
		return
	}
	if m.catchup != nil {
		m.catchup.forgetCharge(vehicleID, c.id)
	}
	if m.chargesStore == nil {
		return
	}

	// Phantom-charge guard. A session with zero wall-clock duration AND
	// zero SoC delta is by definition not a charge: it's a single sticky
	// chargerState frame that opened and closed in the same tick. Real
	// charges advance one of the two within seconds (the open frame is
	// zero-delta too, but the very next ongoing frame moves endAt). Skip
	// the upsert so neither the open nor the same-tick close path can
	// persist a 0m / 70%→70% row. See v0.10.x phantom-charges incident.
	if !c.endAt.After(c.startedAt) && c.endSoC == c.startSoC {
		return
	}

	if c.rejected && !m.prepareRejectedCharge(ctx, vehicleID, c) {
		return
	}
	row := m.liveChargeRow(vehicleID, c)
	if err := m.chargesStore.Upsert(ctx, row); err != nil {
		if sessionOverlapRejected(err) {
			if !c.rejected {
				m.logger.Warn("live charge rejected: overlaps another live charge for this vehicle (duplicate recorder?); retrying with a trimmed start",
					"vehicle", vehicleID, "id", c.id, "err", err.Error())
			}
			c.rejected, c.rejectedAt = true, time.Now()
			return
		}
		m.logger.Debug("live charge upsert failed", "vehicle", vehicleID, "id", c.id, "err", err.Error())
		return
	}
	if c.rejected {
		c.rejected = false
		m.logger.Info("rejected live charge written after the overlapping one", "vehicle", vehicleID, "id", c.id, "from", row.StartedAt)
	}
}

// liveChargeRow folds the in-flight liveCharge accumulator + any
// bonded LiveSession push into a persistable charges.Charge row.
// Extracted so the post-close hook can snapshot the same shape that
// upsertLiveCharge writes without having to re-implement the merge.
func (m *StateMonitor) liveChargeRow(vehicleID string, c *liveCharge) charges.Charge {
	// Prefer real metrics from Rivian's live session feed when we
	// have them. As of v0.3.6 this map is populated by BOTH the REST
	// chargingSessionPoller (Rivian chargers / select DC fast) and
	// the WebSocket ChargingSession subscription (every session type
	// including home AC / L1 / L2), so for an active charge we
	// generally have pushed totals within seconds of the session
	// starting.
	m.mu.RLock()
	liveSess := m.lastSession[vehicleID]
	bondedTo := m.lastSessionFor[vehicleID]
	m.mu.RUnlock()

	// Bond check. The cached LiveSession is only credited to this
	// charge when applyLiveSession stamped it with this charge's id
	// at the time of the push. Anything else -- a stale Parallax
	// replay arriving between sessions, a frame received before
	// chargeBond was set on session open, or a frame from a previous
	// charge that lingered in the cache -- gets dropped here so its
	// TotalChargedEnergyKWh / RangeAddedKm / PowerKW can't leak into
	// the wrong row. Replaces an earlier StartTime-proximity filter
	// that silently passed empty/invalid StartTime values from the
	// Parallax feed (root cause of the v0.17.x phantom charge
	// incident: stale 4.10 kWh inherited by a 3.5s chargerState
	// glitch session).
	if liveSess != nil && bondedTo != c.id {
		liveSess = nil
	}

	var energy, milesAdded, maxPower float64
	if c.maxPower <= maxLivePowerKW {
		maxPower = c.maxPower
	}
	if liveSess != nil && liveSess.Active {
		if liveSess.PowerKW > maxPower && liveSess.PowerKW <= maxLivePowerKW {
			maxPower = liveSess.PowerKW
		}
		// A session total is credited only with power behind it: the
		// cloud replays a finished session's snapshot (its total, marked
		// active) on every resubscribe, and with nothing drawing a
		// replayed 36.8 kWh used to land on a 0 kW plugged-in charge.
		// Nor when the session began well before this charge:
		// Parallax's charge breakdown counts per plug-in, so a car left
		// plugged in for days reports the whole plug-in's running total
		// (69.9 kWh at 75 h, seeded into 2-s charges).
		if maxPower > 0 && !sessionPredatesCharge(liveSess, c) {
			if liveSess.TotalChargedEnergyKWh > energy {
				energy = liveSess.TotalChargedEnergyKWh
			}
			if liveSess.RangeAddedKm > 0 {
				milesAdded = liveSess.RangeAddedKm * kmToMi
			}
		}
	}

	// L2 energy: trapezoidal integral of chargerPowerKW × wall-clock.
	// Parallax never emits TotalChargedEnergyKWh for AC home sessions,
	// but vehicleState pushes chargerPowerKW frame-by-frame the entire
	// time. Gated on peak power < l2PowerThresholdKW so a DCFC session
	// that's transiently missing Parallax energy doesn't get its
	// rolling integral (which would lag the true delivered kWh
	// because of frame-cadence undersampling at high power) onto the
	// row instead of Rivian's authoritative number.
	if energy == 0 && c.maxPower > 0 && c.maxPower < l2PowerThresholdKW && c.energyIntKWh > 0 {
		energy = c.energyIntKWh
	}

	// SoC-delta last resort. If neither Parallax nor the live integral
	// produced an energy reading (e.g. session opened on a frame whose
	// power was already 0, or pack-only events), back into kWh from
	// the SoC delta times the per-vehicle pack capacity. Without this
	// the row lands with max_power_kw set but energy_added_kwh / cost
	// NULL, which surfaced as "charging is broken" on the UI.
	if energy == 0 {
		dSoC := c.endSoC - c.startSoC
		if dSoC > 0 {
			if pack := m.PackKWhFor(vehicleID); pack > 0 {
				energy = dSoC / 100.0 * pack
			}
		}
	}

	// Physical plausibility cap (L2/home only). Delivered energy can
	// never exceed peak observed power × elapsed time. Rivian's Parallax
	// feed reports TotalChargedEnergyKWh cumulatively since the cable was
	// plugged in, so a brief top-up fragment — the car reaches
	// charging_complete, does a short charging_active cycle, then
	// completes again, opening a fresh session — would otherwise inherit
	// the whole plug-in's total (e.g. 68 kWh credited to a 10-min 7.5 kW
	// session). When the chosen energy exceeds the physical ceiling on an
	// L2 session, prefer the observed-power integral (accurate at these
	// rates), then the SoC-delta, then the ceiling. Scoped to L2 because
	// at DCFC the integral undersamples (frame cadence) and the Parallax
	// total is authoritative and per-stop, not fragmented.
	//
	// Every session is capped, not just L2: a stale or replayed total
	// on any charge can't exceed what the observed peak could deliver.
	// At DC fast the frame cadence undersamples the peak, hence the
	// wider margin there.
	if maxPower > 0 {
		if hours := c.endAt.Sub(c.startedAt).Hours(); hours > 0 {
			margin := 1.15
			if maxPower >= l2PowerThresholdKW {
				margin = 1.5
			}
			physCap := maxPower * hours * margin
			if energy > physCap {
				socDeltaKWh := 0.0
				if dSoC := c.endSoC - c.startSoC; dSoC > 0 {
					if pack := m.PackKWhFor(vehicleID); pack > 0 {
						socDeltaKWh = dSoC / 100.0 * pack
					}
				}
				switch {
				case c.energyIntKWh > 0 && c.energyIntKWh <= physCap:
					energy = c.energyIntKWh
				case socDeltaKWh > 0 && socDeltaKWh <= physCap:
					energy = socDeltaKWh
				default:
					energy = physCap
				}
			}
		}
	}

	// Session average = energy delivered ÷ wall-clock duration. Folds
	// in ramp-up, taper, and any idle gaps. Cap at maxLivePowerKW
	// because a stale TotalChargedEnergyKWh leaking into a very
	// short same-tick session produced 25000+ kW averages on
	// charge rows in the past.
	avg := 0.0
	if hours := c.endAt.Sub(c.startedAt).Hours(); hours > 0 && energy > 0 {
		avg = energy / hours
		if avg > maxLivePowerKW {
			avg = maxLivePowerKW
		}
	}
	// With no power observed, the average (from the SoC delta) stands
	// in for the peak, but only at AC rates: a short session with a
	// jumpy SoC must not come out looking like a DC fast charge.
	if maxPower == 0 && avg > 0 && avg < l2PowerThresholdKW {
		maxPower = avg
	}

	row := charges.Charge{
		ID:             c.id,
		VehicleID:      vehicleID,
		StartedAt:      c.startedAt,
		EndedAt:        c.endAt,
		StartSoCPct:    c.startSoC,
		EndSoCPct:      c.endSoC,
		EnergyAddedKWh: energy,
		MilesAdded:     milesAdded,
		MaxPowerKW:     maxPower,
		AvgPowerKW:     avg,
		FinalState:     c.finalState,
		Lat:            c.lat,
		Lon:            c.lon,
		Source:         "live",
		ActiveSeconds:  c.activeSeconds,
	}
	// Parallax-only thermal breakdown. Only set when the live session
	// has actually reported a thermal_kwh value (>0) — otherwise leave
	// nil so legacy / non-Parallax paths don't synthesize fake zeros.
	if liveSess != nil && liveSess.ThermalKWh > 0 {
		v := liveSess.ThermalKWh
		row.ThermalKWh = &v
	}
	// What Rivian says about the session (its own charger, its price)
	// belongs to this charge only if the session is this charge's:
	// getLiveSessionHistory can still return the previous session.
	stale := sessionPredatesCharge(liveSess, c)
	if liveSess != nil && !stale && liveSess.RivianChargerKnown {
		v := liveSess.IsRivianCharger
		row.RivianCharger = &v
	}
	// Snapshot cost. Rivian-reported RAN / Wall Charger prices win
	// (they're the real billed amount). A DC fast charge Rivian didn't
	// bill (Tesla, Electrify America, ...) is priced at the network
	// rate of the station it happened at; anything slower uses the
	// user's home $/kWh rate. Persisting means future rate changes
	// don't retroactively rewrite history; CostSource records which.
	if liveSess != nil && !stale && liveSess.CurrentPrice != "" {
		if cost, err := strconv.ParseFloat(liveSess.CurrentPrice, 64); err == nil && cost > 0 {
			row.Cost = cost
			row.Currency = liveSess.CurrentCurrency
			row.CostSource = CostSourceRivian
			// The billing network when the session names it, so the
			// charge map labels a Rivian-billed stop by its network
			// instead of the nearest charger's brand. Never guessed from
			// "Rivian-billed" alone: Rivian can bill other networks too.
			// Rivian's own charger at DC fast speed is the Rivian
			// Adventure Network; at AC it could be a Waypoints or Wall
			// Charger, so that stays unnamed.
			row.Network = strings.TrimSpace(liveSess.Network)
			if row.Network == "" && liveSess.RivianChargerKnown && liveSess.IsRivianCharger &&
				IsDCFCSession(row.MaxPowerKW, energy, row.ActiveSeconds, row.EndedAt.Sub(row.StartedAt)) {
				row.Network = NetworkRivianAdventure
			}
			if energy > 0 {
				row.PricePerKWh = cost / energy
			}
		}
	}
	if row.Cost == 0 && energy > 0 {
		m.mu.RLock()
		lookup, dcfc, home, detect := m.priceLookup, m.dcfcPrice, m.homeLookup, m.dcfcDetect
		m.mu.RUnlock()
		fast := (dcfc != nil || detect != nil) && IsDCFCSession(row.MaxPowerKW, energy, row.ActiveSeconds, row.EndedAt.Sub(row.StartedAt))
		// Network detection with a verify state, where it's on for the
		// user. Rivian's isRivianCharger (this charge's own session,
		// see sessionPredatesCharge) is its strongest signal, so a
		// verdict made without it is made again once it arrives.
		ranCharger := row.RivianCharger != nil && *row.RivianCharger
		if fast && detect != nil && (c.dcfcVerdict == nil ||
			(ranCharger && !c.dcfcRivianCharger) ||
			(c.dcfcVerdict.Confidence < dcfcrates.ConfidentAt && row.MaxPowerKW > c.dcfcPeakKW*1.1)) {
			if v, on := detect(c.lat, c.lon, ranCharger, row.MaxPowerKW); on {
				c.dcfcVerdict, c.dcfcPeakKW, c.dcfcRivianCharger = &v, row.MaxPowerKW, ranCharger
			}
		}
		if fast && c.dcfcVerdict != nil {
			v := c.dcfcVerdict
			row.NetworkState, row.NetworkEvidence = v.State, v.Evidence
			conf := v.Confidence
			row.NetworkConfidence = &conf
			if v.Priced && v.Rate > 0 {
				row.PricePerKWh = v.Rate
				row.Currency = v.Currency
				row.Cost = v.Rate * energy
				row.CostSource = CostSourceNetworkEstimate
				row.Network = v.Network
			}
			// Unpriced (nothing matched): the row waits for the user,
			// network_state says so.
		} else if fast && dcfc != nil {
			if !c.dcfcPriced {
				if rate, cur, network, ok := dcfc(row.Lat, row.Lon); ok && rate > 0 {
					c.dcfcPriced, c.dcfcRate, c.dcfcCur, c.dcfcNetwork = true, rate, cur, network
				}
			}
			if c.dcfcPriced {
				row.PricePerKWh = c.dcfcRate
				row.Currency = c.dcfcCur
				row.Cost = c.dcfcRate * energy
				row.CostSource = CostSourceNetworkEstimate
				row.Network = c.dcfcNetwork
			}
			// No station matched (or the archive isn't loaded): leave
			// it unpriced rather than at the home rate or a guessed
			// network. The ledger shows "unknown", and `rivolt reprice`
			// can price it later; nothing is stamped.
		}
		// AC charging away from home (a destination L1/L2, a friend's
		// outlet, a paid public L2) has no known price: leave it unpriced
		// for the user to fill in rather than stamp the home rate on it.
		// Without a saved home location or a session position we can't
		// tell, so slow charging keeps the home rate.
		if !fast && row.Cost == 0 && lookup != nil && !AwayFromHome(row.Lat, row.Lon, home) {
			if rate, cur := lookup(); rate > 0 {
				row.PricePerKWh = rate
				row.Currency = cur
				row.Cost = rate * energy
				row.CostSource = CostSourceHomeRate
			}
		}
	}
	// Snapshot where it happened (migration 0044) so the ledger keeps
	// classifying it the same way if the user later moves home.
	m.mu.RLock()
	homeNow := m.homeLookup
	m.mu.RUnlock()
	row.Place = PlaceOf(row.Lat, row.Lon, homeNow)
	trimCharge(&row, c.trim)
	return row
}

// isDrivingGear is true for any non-park gear. Empty ("") is treated
// as parked so a missing/unknown value doesn't spuriously open a
// drive session on startup.
// DCFCMinKW is the session peak power at which a charge counts as DC
// fast charging — the same line analytics.DefaultParams draws for the
// Fast bucket (no AC L2 hardware exceeds ~22 kW).
const DCFCMinKW = 50.0

// DCFCMinEffectiveKW is the energy-over-charging-time floor a session
// must also clear. Recorded peaks glitch: prod has 15-hour home L2
// sessions with a single "145 kW" sample. Real DC fast charging
// averages well above any AC charger's ~11-19 kW.
const DCFCMinEffectiveKW = 20.0

// DCFCMinKWh is the least energy a session must deliver to count as DC
// fast charging. A few-second fragment of a home session can carry a
// spurious high-kW sample, and over 4 s even 0.12 kWh averages ~110 kW
// (seen on preview), so the effective-power test alone can't reject it.
// A real fast-charging stop delivers tens of kWh; anything under 1 kWh
// isn't worth pricing as one.
const DCFCMinKWh = 1.0

// IsDCFCSession reports whether a charge was DC fast charging: peak at
// or above DCFCMinKW, at least DCFCMinKWh delivered, AND energy divided
// by active charging time (wall span when that's unknown) at or above
// DCFCMinEffectiveKW.
func IsDCFCSession(peakKW, energyKWh, activeSeconds float64, span time.Duration) bool {
	if peakKW < DCFCMinKW || energyKWh < DCFCMinKWh {
		return false
	}
	secs := activeSeconds
	if secs <= 0 {
		secs = span.Seconds()
	}
	if secs <= 0 {
		return false
	}
	return energyKWh/(secs/3600) >= DCFCMinEffectiveKW
}

// HomeRadiusM is how far from the saved home location a charge can start
// and still count as charging at home. Shared with the ledger so the
// recorder's pricing and the Money tab's Home/Away agree.
const HomeRadiusM = 250.0

// AwayFromHome reports whether (lat, lon) is known to be more than
// HomeRadiusM from the saved home location. False when either the home
// location or the position is unknown.
func AwayFromHome(lat, lon float64, home HomeLookup) bool {
	return PlaceOf(lat, lon, home) == PlaceAway
}

// Values of charges.Charge.Place (migration 0044).
const (
	PlaceHome = "home"
	PlaceAway = "away"
)

// PlaceOf classifies (lat, lon) against the saved home location:
// PlaceHome within HomeRadiusM, PlaceAway beyond it, "" when either the
// home location or the position is unknown.
func PlaceOf(lat, lon float64, home HomeLookup) string {
	if home == nil || (lat == 0 && lon == 0) {
		return ""
	}
	hLat, hLon, ok := home()
	if !ok {
		return ""
	}
	if haversineMeters(hLat, hLon, lat, lon) > HomeRadiusM {
		return PlaceAway
	}
	return PlaceHome
}

// Values of charges.Charge.CostSource (migration 0040).
const (
	CostSourceRivian          = "rivian"
	CostSourceHomeRate        = "home_rate"
	CostSourceNetworkEstimate = "network_estimate"
	CostSourceUser            = "user"
)

func isDrivingGear(g string) bool {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "D", "R", "N":
		return true
	}
	return false
}

// isChargingCS reports whether a Rivian chargerState string indicates
// an ongoing charging session. Matches home-assistant-rivian's charging
// sensor logic: anything with "charging_" prefix except terminal
// states. charging_ready counts because the car is physically plugged
// in and negotiating — power will come next.
func isChargingCS(s string) bool {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return false
	}
	switch v {
	case "charger_disconnected", "charging_complete", "charging_user_stopped",
		"charging_station_err", "charging_user_stoppe",
		// Plugged in, waiting for a scheduled start: no energy flows.
		// It used to open a live charge that the charging-session feed's
		// replay of the previous session's total then filled (eleven
		// 36.8 kWh copies at 0 kW in one night, 01-248088167 Jul 13);
		// the real session opens on charging_active.
		"charging_scheduled":
		return false
	}
	return strings.HasPrefix(v, "charging_") || v == "waiting_on_charger"
}

// plugged reports whether the cable is connected for the lifecycle.
// Rivian's chargerStatus says so when the vehicle reports it. An R2
// never does (charger_status is empty on every R2 row), so there a
// charging state that only exists with power flowing stands in:
// charging_active or charging_connecting with power above zero, or
// with a charge already open (a 0 kW pause mid-session doesn't split
// it). charging_ready alone never counts: an R2 reports it while
// driving and parked unplugged. Leaving those states (charging_complete,
// charging_ready, disconnected) reads as unplugged, which closes the
// charge as before. Must be called with m.sessMu held.
func (s *liveSessions) plugged(curr *State) bool {
	if strings.TrimSpace(curr.ChargerStatus) != "" {
		return isPluggedCS(curr.ChargerStatus)
	}
	switch strings.ToLower(strings.TrimSpace(curr.ChargerState)) {
	case "charging_active", "charging_connecting":
		return curr.ChargerPowerKW > 0 || s.charge != nil
	}
	return false
}

// isPluggedCS reports whether Rivian's chargerStatus field indicates
// the cable is physically connected. Anything starting with
// 'chrgr_sts_connected' means plugged in (charging or negotiating);
// 'chrgr_sts_not_connected' (and the empty string) means unplugged.
func isPluggedCS(s string) bool {
	v := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(v, "chrgr_sts_connected")
}

// liveSessionID builds a deterministic ID for a live-derived drive or
// charge so re-upserts against the same session collapse to one row.
// Keyed on the session start timestamp (Unix seconds) so a restarted
// process that rehydrates from cache can't create a duplicate as long
// as it sees the same start time.
func liveSessionID(vehicleID, kind string, t time.Time) string {
	return fmt.Sprintf("live_%s_%s_%d", vehicleID, kind, t.UTC().Unix())
}

// resumeOpenCharge looks for a live-sourced charge row for this
// vehicle that the recorder left in an open (non-terminal) state —
// typically because the process was killed mid-session. Returns the
// rehydrated liveCharge accumulator on hit, nil on miss (including
// store errors, since failing to reattach just falls through to
// opening a new session).
//
// Only charges that started within the last 24h are considered — an
// older open row is almost certainly a recorder bug or a genuinely
// lost session we shouldn't keep appending to.
func (m *StateMonitor) resumeOpenCharge(ctx context.Context, curr *State) *liveCharge {
	if m.chargesStore == nil || curr == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	row, err := m.chargesStore.LatestOpenLive(rctx, curr.VehicleID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			m.logger.Debug("charge resume lookup failed", "vehicle", curr.VehicleID, "err", err.Error())
		}
		return nil
	}
	if row == nil || time.Since(row.StartedAt) > 24*time.Hour {
		return nil
	}
	c := &liveCharge{
		id:         row.ID,
		startedAt:  row.StartedAt,
		startSoC:   row.StartSoCPct,
		lat:        row.Lat,
		lon:        row.Lon,
		maxPower:   row.MaxPowerKW,
		endAt:      row.EndedAt,
		endSoC:     row.EndSoCPct,
		finalState: row.FinalState,
		// Continue the active-charging-time accumulator from the
		// persisted value so a mid-charge restart doesn't reset it.
		activeSeconds: row.ActiveSeconds,
		// Anchor the stale-session guard at the last advanced frame, not
		// zero. A zero lastMeaningfulAt makes the guard fall back to
		// startedAt and abandon any resumed charge older than the gap
		// window on the first idle frame after a restart.
		lastMeaningfulAt: row.EndedAt,
	}
	m.logger.Info("resumed open charge from DB",
		"vehicle", curr.VehicleID,
		"id", row.ID,
		"started_at", row.StartedAt,
		"age", time.Since(row.StartedAt).Round(time.Second))
	return c
}

// closeStaleOpenCharges marks every live charge row for the vehicle
// OTHER than keepID whose final_state is still "charging_*" as
// abandoned. Retires orphans created by previous restarts so they
// don't show up as duplicate active sessions in the UI. Best-effort
// — failures are logged and swallowed.
func (m *StateMonitor) closeStaleOpenCharges(ctx context.Context, vehicleID, keepID string) {
	if m.chargesStore == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	n, err := m.chargesStore.CloseStaleOpenLive(cctx, vehicleID, keepID)
	if err != nil {
		m.logger.Debug("stale charge cleanup failed", "vehicle", vehicleID, "err", err.Error())
		return
	}
	if n > 0 {
		m.logger.Info("closed stale open charges", "vehicle", vehicleID, "count", n, "kept", keepID)
	}
}

// minNonZero4 returns the smallest of the four arguments, ignoring
// zeros. Returns 0 only if all four are zero (sensor outage / sample
// taken before any TPMS reading has cycled). Used by the live
// recorder to compress the four tire-pressure corners into a single
// "worst tire" value persisted to vehicle_state.
func minNonZero4(a, b, c, d float64) float64 {
	min := 0.0
	for _, v := range [4]float64{a, b, c, d} {
		if v <= 0 {
			continue
		}
		if min == 0 || v < min {
			min = v
		}
	}
	return min
}

// readyUnreliable reports a vehicle whose charging_ready doesn't mean
// plugged in: an R2. R1s also report charging_ready while driving, but
// their charge open/close already relies on it, so the rule stays
// R2-only.
func (m *StateMonitor) readyUnreliable(vehicleID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := m.vehicleInfo[vehicleID]
	return v != nil && strings.EqualFold(strings.TrimSpace(v.Model), "R2")
}
