package rivian

import (
	"context"
	"math"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
)

// Rejected live sessions (overlap guard, migrations 0041/0043).
//
// When the database refuses a live charge or drive because another
// recorder already wrote an overlapping one for the vehicle, this
// recorder must not simply give up: in a split-brain the first writer
// may be the ghost, and if it dies mid-session the rest of the session
// exists only here. So a rejected session keeps accumulating, and every
// rejectedRetryEvery (and once more at close) it looks up the
// conflicting row and, if this session runs past that row's end, writes
// just the tail: start moved to the other row's end, start SoC /
// odometer taken from it, and (charges) the energy it already holds
// subtracted. If the other recorder is still covering everything, the
// session stays rejected and its close hooks don't fire, so the
// duplicate recorder sends no duplicate "charging done" push.

// rejectedRetryEvery is how often a rejected in-session row retries.
const rejectedRetryEvery = 10 * time.Minute

// sessionTrim rebases a rejected session's row onto the end of the live
// row it overlapped, so only the part after it is written.
type sessionTrim struct {
	start      time.Time
	startSoC   float64
	startOdoMi float64 // drives
	energyKWh  float64 // charges: energy the other row already holds
}

// rejectedRetryDue reports whether a rejected session should try to
// write again now.
func rejectedRetryDue(rejectedAt, now time.Time, final bool) bool {
	return final || now.Sub(rejectedAt) >= rejectedRetryEvery
}

// trimDrive applies t to a drive row built from the full accumulator.
func trimDrive(row *drives.Drive, t *sessionTrim, packKWh float64) {
	if t == nil {
		return
	}
	row.StartedAt = t.start
	row.StartLat, row.StartLon = 0, 0 // unknown at the trim point
	if t.startOdoMi > 0 {
		row.StartOdometerMi = t.startOdoMi
		row.DistanceMi = row.EndOdometerMi - t.startOdoMi
		if row.DistanceMi < 0 {
			row.DistanceMi = 0
		}
	}
	if t.startSoC > 0 {
		row.StartSoCPct = t.startSoC
		row.EnergyUsedKWh = 0
		if used := t.startSoC - row.EndSoCPct; used > 0 && packKWh > 0 {
			row.EnergyUsedKWh = used / 100 * packKWh
		}
	}
}

// trimCharge applies t to a charge row built from the full accumulator.
//
// The tail's energy is scaled by SoC: ours × (our end SoC − the other
// row's end SoC) ÷ (our end SoC − our start SoC). That doesn't depend on
// what each row's energy figure is measured from. Subtracting the other
// row's energy is only right when both carry the same cumulative base
// (Rivian's TotalChargedEnergyKWh for one plug-in); when the other
// recorder started earlier and integrated its own power or SoC, its
// figure includes kWh from before our start and subtraction undercounts
// (devils-advocate: ghost 10:00–11:00 = 30 kWh, ours 10:30–12:00 = 45 kWh
// → the tail is ~30, not 15). Subtraction is the fallback only when SoC
// isn't known. Cost scales with the energy that remains.
func trimCharge(row *charges.Charge, t *sessionTrim) {
	if t == nil {
		return
	}
	ourStart, ourEnd := row.StartSoCPct, row.EndSoCPct
	row.StartedAt = t.start
	if row.EnergyAddedKWh > 0 {
		var rest float64
		if ourStart > 0 && ourEnd > ourStart && t.startSoC > 0 {
			rest = row.EnergyAddedKWh * (ourEnd - t.startSoC) / (ourEnd - ourStart)
		} else {
			rest = row.EnergyAddedKWh - t.energyKWh
		}
		rest = math.Max(0, math.Min(rest, row.EnergyAddedKWh))
		if row.Cost > 0 {
			row.Cost *= rest / row.EnergyAddedKWh
		}
		row.EnergyAddedKWh = rest
	}
	if t.startSoC > 0 {
		row.StartSoCPct = t.startSoC
	}
}

// prepareRejectedDrive decides whether a rejected drive should be
// written now and, if so, sets its trim. Returns false to skip.
func (m *StateMonitor) prepareRejectedDrive(ctx context.Context, vehicleID string, d *liveDrive) bool {
	if !rejectedRetryDue(d.rejectedAt, time.Now(), d.final) {
		return false
	}
	d.rejectedAt = time.Now()
	ov, err := m.drivesStore.LiveOverlap(ctx, vehicleID, d.id, d.startedAt, d.endAt)
	if err != nil {
		m.logger.Debug("rejected drive: overlap lookup failed", "vehicle", vehicleID, "id", d.id, "err", err.Error())
		return false
	}
	if ov == nil { // the conflicting row is gone: write the whole session
		d.trim = nil
		return true
	}
	if !ov.EndedAt.Before(d.endAt) { // still fully covered by the other recorder
		return false
	}
	d.trim = &sessionTrim{start: ov.EndedAt, startSoC: ov.EndSoCPct, startOdoMi: ov.EndOdometerMi}
	return true
}

// prepareRejectedCharge is prepareRejectedDrive for charges.
func (m *StateMonitor) prepareRejectedCharge(ctx context.Context, vehicleID string, c *liveCharge) bool {
	if !rejectedRetryDue(c.rejectedAt, time.Now(), c.final) {
		return false
	}
	c.rejectedAt = time.Now()
	ov, err := m.chargesStore.LiveOverlap(ctx, vehicleID, c.id, c.startedAt, c.endAt)
	if err != nil {
		m.logger.Debug("rejected charge: overlap lookup failed", "vehicle", vehicleID, "id", c.id, "err", err.Error())
		return false
	}
	if ov == nil {
		c.trim = nil
		return true
	}
	if !ov.EndedAt.Before(c.endAt) {
		return false
	}
	c.trim = &sessionTrim{start: ov.EndedAt, startSoC: ov.EndSoCPct, energyKWh: ov.EnergyKWh}
	return true
}
