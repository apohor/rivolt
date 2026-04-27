package rivian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// Conversion factors between the wire (metric) and the samples store
// (imperial, inherited from the ElectraFi importer schema).
const (
	kmToMi  = 0.621371
	kphToMi = 0.621371
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
}

type liveCharge struct {
	id        string
	startedAt time.Time
	number    int64
	startSoC  float64
	lat       float64
	lon       float64
	maxPower  float64 // kW
	sumPower  float64
	powerN    int

	endAt      time.Time
	endSoC     float64
	finalState string
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
		sess = &liveSessions{}
		m.sessions[vehicleID] = sess
	}

	// Handle session lifecycle FIRST so the sample row carries the
	// right drive_number / charge_number for this frame.
	driveNum := sess.handleDriveLifecycle(curr, prev, m, wctx)
	chargeNum := sess.handleChargeLifecycle(curr, prev, m, wctx)
	m.sessMu.Unlock()

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
			ChargingState:   curr.ChargerState,
			ChargerPowerKW:  curr.ChargerPowerKW,
			ChargeLimitPct:  curr.ChargeTargetPct,
			InsideTempC:     curr.CabinTempC,
			OutsideTempC:    curr.OutsideTempC,
			DriveNumber:     driveNum,
			ChargeNumber:    chargeNum,
			Source:          "live",
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

// handleDriveLifecycle manages the drive accumulator across a single
// state transition. Returns the drive_number to stamp on this frame's
// sample row (0 if not currently driving).
//
// Must be called with m.sessMu held.
func (s *liveSessions) handleDriveLifecycle(curr, prev *State, m *StateMonitor, ctx context.Context) int64 {
	_ = prev // reserved for future transition-aware logic (e.g. only upserting on real changes).
	driving := isDrivingGear(curr.Gear)

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
		s.drive.endAt = curr.At
		s.drive.endSoC = curr.BatteryLevelPct
		if odoMi := curr.OdometerKm * kmToMi; odoMi > 0 {
			s.drive.endOdoMi = odoMi
		}
		if curr.Latitude != 0 || curr.Longitude != 0 {
			s.drive.endLat = curr.Latitude
			s.drive.endLon = curr.Longitude
		}
		// Periodically re-upsert so a crash preserves the latest
		// state. Cheap: single-row upsert against a sub-1k-row table.
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		return s.drive.number
	}

	// Close drive on transition D/R/N → P.
	if !driving && s.drive != nil {
		m.upsertLiveDrive(ctx, curr.VehicleID, s.drive)
		n := s.drive.number
		s.drive = nil
		return n
	}
	return 0
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
			// row advances forward on the next upsert.
			s.charge.endAt = curr.At
			s.charge.endSoC = curr.BatteryLevelPct
			s.charge.finalState = curr.ChargerState
			if curr.ChargerPowerKW > s.charge.maxPower {
				s.charge.maxPower = curr.ChargerPowerKW
			}
			m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
			m.closeStaleOpenCharges(ctx, curr.VehicleID, s.charge.id)
			return s.charge.number
		}
		s.chargeCounter++
		s.charge = &liveCharge{
			id:         liveSessionID(curr.VehicleID, "c", curr.At),
			startedAt:  curr.At,
			number:     s.chargeCounter,
			startSoC:   curr.BatteryLevelPct,
			lat:        curr.Latitude,
			lon:        curr.Longitude,
			endAt:      curr.At,
			endSoC:     curr.BatteryLevelPct,
			finalState: curr.ChargerState,
		}
		if curr.ChargerPowerKW > 0 {
			s.charge.maxPower = curr.ChargerPowerKW
			s.charge.sumPower = curr.ChargerPowerKW
			s.charge.powerN = 1
		}
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		m.closeStaleOpenCharges(ctx, curr.VehicleID, s.charge.id)
		return s.charge.number
	}

	// Charge ongoing: update running aggregates.
	if charging && s.charge != nil {
		if curr.ChargerPowerKW > s.charge.maxPower {
			s.charge.maxPower = curr.ChargerPowerKW
		}
		if curr.ChargerPowerKW > 0 {
			s.charge.sumPower += curr.ChargerPowerKW
			s.charge.powerN++
		}
		s.charge.endAt = curr.At
		s.charge.endSoC = curr.BatteryLevelPct
		s.charge.finalState = curr.ChargerState
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
		return s.charge.number
	}

	// Close charge on terminal state.
	if !charging && s.charge != nil {
		s.charge.finalState = curr.ChargerState
		m.upsertLiveCharge(ctx, curr.VehicleID, s.charge)
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
		m.mu.Unlock()
		return n
	}
	return 0
}

func (m *StateMonitor) upsertLiveDrive(ctx context.Context, vehicleID string, d *liveDrive) {
	if m.drivesStore == nil || d == nil {
		return
	}
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
	}
	if err := m.drivesStore.Upsert(ctx, row); err != nil {
		m.logger.Debug("live drive upsert failed", "vehicle", vehicleID, "id", d.id, "err", err.Error())
	}
}

func (m *StateMonitor) upsertLiveCharge(ctx context.Context, vehicleID string, c *liveCharge) {
	if m.chargesStore == nil || c == nil {
		return
	}
	avg := 0.0
	if c.powerN > 0 {
		avg = c.sumPower / float64(c.powerN)
	}

	// Prefer real metrics from Rivian's live session feed when we
	// have them. As of v0.3.6 this map is populated by BOTH the REST
	// chargingSessionPoller (Rivian chargers / select DC fast) and
	// the WebSocket ChargingSession subscription (every session type
	// including home AC / L1 / L2), so for an active charge we
	// generally have pushed totals within seconds of the session
	// starting.
	m.mu.RLock()
	liveSess := m.lastSession[vehicleID]
	m.mu.RUnlock()

	var energy, milesAdded, maxPower float64
	maxPower = c.maxPower
	if liveSess != nil && liveSess.Active {
		if liveSess.TotalChargedEnergyKWh > energy {
			energy = liveSess.TotalChargedEnergyKWh
		}
		if liveSess.RangeAddedKm > 0 {
			milesAdded = liveSess.RangeAddedKm * kmToMi
		}
		if liveSess.PowerKW > maxPower {
			maxPower = liveSess.PowerKW
		}
	}

	// SoC-delta fallback for home AC charging: Rivian's live endpoints
	// don't report charger_power or energy_added for those sessions.
	// Estimate energy from the SoC delta × pack capacity and back-fill
	// avg/max power from elapsed time. Same fallback the ElectraFi
	// importer uses for post-2026-03-24 sessions.
	if energy == 0 && maxPower == 0 {
		dSoC := c.endSoC - c.startSoC
		if dSoC > 0 {
			energy = dSoC / 100.0 * m.PackKWhFor(vehicleID)
			hours := c.endAt.Sub(c.startedAt).Hours()
			if hours > 0 && energy > 0 {
				avg = energy / hours
				maxPower = avg
			}
		}
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
	}
	// Snapshot cost. Rivian-reported RAN / Wall Charger prices win
	// (they're the real billed amount); otherwise use the operator's
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
	if err := m.chargesStore.Upsert(ctx, row); err != nil {
		m.logger.Debug("live charge upsert failed", "vehicle", vehicleID, "id", c.id, "err", err.Error())
	}
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
	}
	// We don't persist sumPower/powerN, so seed the running average
	// from the stored avg×count-estimate. Using MaxPowerKW as a
	// single-sample seed is a conservative approximation that keeps
	// the avg from collapsing to 0 after restart.
	if row.AvgPowerKW > 0 {
		c.sumPower = row.AvgPowerKW
		c.powerN = 1
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
