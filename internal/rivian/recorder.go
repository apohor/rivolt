package rivian

import (
	"context"
	"fmt"
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
	endAt      time.Time
	endSoC     float64
	endOdoMi   float64
	endLat     float64
	endLon     float64
}

type liveCharge struct {
	id         string
	startedAt  time.Time
	number     int64
	startSoC   float64
	lat        float64
	lon        float64
	maxPower   float64 // kW
	sumPower   float64
	powerN     int

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

	// Use a detached context with a short timeout so recorder writes
	// can't block cache updates on a slow disk. Use context.Background
	// because the caller's ctx may be about to be cancelled (e.g. on
	// subscription shutdown) and we still want the last sample to land.
	wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
			m.logger.Debug("live sample insert failed", "vehicle", vehicleID, "err", err.Error())
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
	charging := isChargingCS(curr.ChargerState)

	// Open new charge.
	if charging && s.charge == nil {
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
	row := charges.Charge{
		ID:             c.id,
		VehicleID:      vehicleID,
		StartedAt:      c.startedAt,
		EndedAt:        c.endAt,
		StartSoCPct:    c.startSoC,
		EndSoCPct:      c.endSoC,
		EnergyAddedKWh: 0, // Not exposed by the live WS/REST feed; filled in by a future /api/live-session tie-in.
		MilesAdded:     0,
		MaxPowerKW:     c.maxPower,
		AvgPowerKW:     avg,
		FinalState:     c.finalState,
		Lat:            c.lat,
		Lon:            c.lon,
		Source:         "live",
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

// liveSessionID builds a deterministic ID for a live-derived drive or
// charge so re-upserts against the same session collapse to one row.
// Keyed on the session start timestamp (Unix seconds) so a restarted
// process that rehydrates from cache can't create a duplicate as long
// as it sees the same start time.
func liveSessionID(vehicleID, kind string, t time.Time) string {
	return fmt.Sprintf("live_%s_%s_%d", vehicleID, kind, t.UTC().Unix())
}
