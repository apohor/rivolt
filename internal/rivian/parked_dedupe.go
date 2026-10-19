package rivian

import (
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/samples"
)

// A parked truck repeats an unchanged state every ~16 s; those rows were 42% of
// vehicle_state. The recorder now writes a parked row only when something
// changed, plus a heartbeat, so every reader still sees the truck within
// parkedHeartbeat (the longest consumer window is plug-in's 15 min and the
// admin "streaming" check's 10 min; drain caps an interval at 20 min).
const (
	parkedHeartbeat = 5 * time.Minute
	parkedSoCStep   = 1.0  // percentage points
	parkedMoveM     = 50.0 // metres
)

// parkedDedupeEnabled is on unless RIVOLT_SAMPLE_PARKED_DEDUPE is set to a false
// value, so it can be turned off without a deploy of code.
func parkedDedupeEnabled() bool {
	v, ok := os.LookupEnv("RIVOLT_SAMPLE_PARKED_DEDUPE")
	if !ok || strings.TrimSpace(v) == "" {
		return true
	}
	return ParseBoolEnv(v)
}

// parkedGate remembers, per vehicle, the last row that was written.
type parkedGate struct {
	mu   sync.Mutex
	last map[string]samples.Sample
}

// isParkedSample is a row with nothing going on: no open drive or charge, gear P
// (or unknown), standing still and not charging. Anything else is always written.
func isParkedSample(s samples.Sample) bool {
	if s.DriveNumber != 0 || s.ChargeNumber != 0 || s.SpeedMph >= 1 {
		return false
	}
	if s.ShiftState != "" && s.ShiftState != "P" {
		return false
	}
	return s.ChargingState != "charging_active" && s.ChargingState != "charging_connecting"
}

// shouldWrite reports whether s is to be persisted, and remembers it if so. A
// parked row is skipped only when it matches the last written row on every
// state field, SoC is within a step, the truck has not moved, and the heartbeat
// is not due.
func (g *parkedGate) shouldWrite(s samples.Sample) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = make(map[string]samples.Sample)
	}
	prev, seen := g.last[s.VehicleID]
	if seen && isParkedSample(s) && isParkedSample(prev) && !parkedChanged(prev, s) {
		return false
	}
	g.last[s.VehicleID] = s
	return true
}

func (g *parkedGate) forget(vehicleID string) {
	g.mu.Lock()
	delete(g.last, vehicleID)
	g.mu.Unlock()
}

func parkedChanged(a, b samples.Sample) bool {
	if b.At.Sub(a.At) >= parkedHeartbeat || b.At.Before(a.At) {
		return true
	}
	if a.ShiftState != b.ShiftState || a.DriveMode != b.DriveMode ||
		a.ChargingState != b.ChargingState || a.ChargerStatus != b.ChargerStatus ||
		a.PowerState != b.PowerState || a.ChargeLimitPct != b.ChargeLimitPct {
		return true
	}
	if math.Abs(a.BatteryLevelPct-b.BatteryLevelPct) >= parkedSoCStep {
		return true
	}
	// Having or losing a fix is a change; so is moving.
	aFix, bFix := a.Lat != 0 || a.Lon != 0, b.Lat != 0 || b.Lon != 0
	if aFix != bFix {
		return true
	}
	return aFix && haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon) >= parkedMoveM
}
