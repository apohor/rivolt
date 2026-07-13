package rivian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/charges"
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

	// Running counters used as samples.Sample.DriveNumber /
	// ChargeNumber. Incremented at each session open so dashboards can
	// group vehicle_state rows by counter without colliding with
	// electrafi-imported numbers (which are scoped per-export).
	driveCounter  int64
	chargeCounter int64
}

type liveDrive struct {
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
	id        string
	startedAt time.Time
	number    int64
	startSoC  float64
	lat       float64
	lon       float64
	maxPower  float64 // kW

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
		sess.applyMutualExclusion(curr, m, wctx)

		// Handle session lifecycle so the sample row carries the
		// right drive_number / charge_number for this frame.
		driveNum = sess.handleDriveLifecycle(curr, prev, m, wctx)
		chargeNum = sess.handleChargeLifecycle(curr, prev, m, wctx)

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
		m.persistLiveState(vehicleID, snap)
	}

	// Sample insert: one row per cache update. WS pushes arrive only
	// on changes, REST refresh fires every 2 min, charging poller
	// every 30s — so this is naturally throttled.
	if m.samplesStore != nil {
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
		if err := m.samplesStore.InsertBatch(wctx, []samples.Sample{s}); err != nil {
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
	chargingNow := isChargingCS(curr.ChargerState) && isPluggedCS(curr.ChargerStatus)
	if driving && s.charge != nil {
		m.logger.Info("closing live charge: gear is driving",
			"vehicle", curr.VehicleID, "id", s.charge.id, "gear", curr.Gear)
		s.charge.finalState = "abandoned"
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		s.charge = nil
		m.mu.Lock()
		delete(m.lastSession, curr.VehicleID)
		delete(m.chargeBond, curr.VehicleID)
		delete(m.lastSessionFor, curr.VehicleID)
		m.mu.Unlock()
	}
	if chargingNow && s.drive != nil {
		m.logger.Info("closing live drive: car is charging",
			"vehicle", curr.VehicleID, "id", s.drive.id, "charger_state", curr.ChargerState)
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		s.drive = nil
	}
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

	// Open new drive on transition P/"" → D/R/N.
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
		if curr.Latitude != 0 || curr.Longitude != 0 {
			s.drive.endLat = curr.Latitude
			s.drive.endLon = curr.Longitude
			// Append to the path, but skip duplicate consecutive
			// points -- Rivian sometimes replays the last cached fix
			// for several frames when the car is parked at a charger
			// at the start/end of a drive, and we don't want a thousand
			// copies of the same coordinate in the encoded polyline.
			n := len(s.drive.path)
			if n == 0 || s.drive.path[n-1][0] != curr.Latitude || s.drive.path[n-1][1] != curr.Longitude {
				// GPS-gap fill: if the last appended fix is far enough
				// behind in time AND the straight-line jump to curr is
				// long enough to look wrong on a map, ask the routing
				// engine for a road-snapped shape and splice it in.
				if n > 0 && m.routeFiller != nil && !s.drive.lastFixAt.IsZero() {
					last := s.drive.path[n-1]
					gap := curr.At.Sub(s.drive.lastFixAt)
					dist := haversineMeters(last[0], last[1], curr.Latitude, curr.Longitude)
					if gap > 10*time.Second && dist > 100 && dist < 50_000 {
						fillCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
						startedAt := time.Now()
						shape, err := m.routeFiller.RouteShape(fillCtx, last, [2]float64{curr.Latitude, curr.Longitude})
						elapsed := time.Since(startedAt)
						cancel()
						if err != nil {
							// Warn, not Debug: a failed fill degrades the
							// rendered route to a straight line across the
							// gap. elapsed_ms distinguishes a timeout (near
							// the 750ms budget) from a fast upstream error.
							m.logger.Warn("route-fill failed",
								"vehicle", curr.VehicleID,
								"gap", gap.Round(time.Second),
								"dist_m", int(dist),
								"elapsed_ms", elapsed.Milliseconds(),
								"err", err.Error())
						} else if len(shape) > 2 {
							// Drop the first vertex (== last) and the
							// last (== curr); we already have the former
							// and we're about to append the latter.
							s.drive.path = append(s.drive.path, shape[1:len(shape)-1]...)
						}
					}
				}
				s.drive.path = append(s.drive.path, [2]float64{curr.Latitude, curr.Longitude})
				s.drive.lastFixAt = curr.At
			}
		}
		// Periodically re-upsert so a crash preserves the latest
		// state. Cheap: single-row upsert against a sub-1k-row table.
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		return s.drive.number
	}

	// Close drive on transition D/R/N → P.
	if !driving && s.drive != nil {
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
		n := s.drive.number
		s.drive = nil
		if !phantom && m.driveCloseHook != nil {
			go m.runDriveCloseHook(closedRow)
		}
		return n
	}
	return 0
}

// runChargeCloseHook invokes the configured ChargeCloseHook with a
// detached context bounded by hookTimeout. Best-effort: panics and
// errors are caught/logged so a buggy hook can't crash the recorder.
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
	charging := isChargingCS(curr.ChargerState) && isPluggedCS(curr.ChargerStatus)

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
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
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
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		// Snapshot the row before the hook fires (same pattern as the
		// drive-close path) and decide whether to skip the hook for
		// phantom rows that upsertLiveCharge already filtered out.
		closedRow := m.liveChargeRow(curr.VehicleID, s.charge)
		phantom := !s.charge.endAt.After(s.charge.startedAt) && s.charge.endSoC == s.charge.startSoC
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
		if !phantom && m.chargeCloseHook != nil {
			go m.runChargeCloseHook(closedRow)
		}
		return n
	}
	return 0
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
	return drives.Drive{
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
}

func (m *StateMonitor) upsertLiveDrive(ctx context.Context, vehicleID string, d *liveDrive) {
	if m.drivesStore == nil || d == nil {
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
	row := m.liveDriveRow(vehicleID, d)
	if err := m.drivesStore.Upsert(ctx, row); err != nil {
		m.logger.Debug("live drive upsert failed", "vehicle", vehicleID, "id", d.id, "err", err.Error())
	}
}

func (m *StateMonitor) upsertLiveCharge(ctx context.Context, vehicleID string, c *liveCharge) {
	if m.chargesStore == nil || c == nil {
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

	row := m.liveChargeRow(vehicleID, c)
	if err := m.chargesStore.Upsert(ctx, row); err != nil {
		m.logger.Debug("live charge upsert failed", "vehicle", vehicleID, "id", c.id, "err", err.Error())
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
		if liveSess.TotalChargedEnergyKWh > energy {
			energy = liveSess.TotalChargedEnergyKWh
		}
		if liveSess.RangeAddedKm > 0 {
			milesAdded = liveSess.RangeAddedKm * kmToMi
		}
		if liveSess.PowerKW > maxPower && liveSess.PowerKW <= maxLivePowerKW {
			maxPower = liveSess.PowerKW
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
	if maxPower > 0 && maxPower < l2PowerThresholdKW {
		if hours := c.endAt.Sub(c.startedAt).Hours(); hours > 0 {
			physCap := maxPower * hours * 1.15
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
	if maxPower == 0 && avg > 0 {
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
	// Snapshot cost. Rivian-reported RAN / Wall Charger prices win
	// (they're the real billed amount); otherwise use the user's
	// configured home $/kWh rate. Persisting means future rate
	// changes don't retroactively rewrite history.
	if liveSess != nil && liveSess.CurrentPrice != "" {
		if cost, err := strconv.ParseFloat(liveSess.CurrentPrice, 64); err == nil && cost > 0 {
			row.Cost = cost
			row.Currency = liveSess.CurrentCurrency
			if energy > 0 {
				row.PricePerKWh = cost / energy
			}
		}
	}
	if row.Cost == 0 && energy > 0 {
		m.mu.RLock()
		lookup := m.priceLookup
		m.mu.RUnlock()
		if lookup != nil {
			if rate, cur := lookup(); rate > 0 {
				row.PricePerKWh = rate
				row.Currency = cur
				row.Cost = rate * energy
			}
		}
	}
	return row
}

// isDrivingGear is true for any non-park gear. Empty ("") is treated
// as parked so a missing/unknown value doesn't spuriously open a
// drive session on startup.
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
		"charging_station_err", "charging_user_stoppe":
		return false
	}
	return strings.HasPrefix(v, "charging_") || v == "waiting_on_charger"
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
