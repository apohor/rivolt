package histimport

import (
	"fmt"
	"time"
)

// Drive is a parsed drive, in the units and zone the rest of Rivolt uses (miles, kWh, percent, UTC). It maps onto drives.Drive; the import
// API sets the id, vehicle and import batch.
type Drive struct {
	StartedAt, EndedAt             time.Time
	StartSoCPct, EndSoCPct         float64
	StartOdometerMi, EndOdometerMi float64
	DistanceMi                     float64
	StartLat, StartLon             float64
	EndLat, EndLon                 float64
	MaxSpeedMph, AvgSpeedMph       float64
	// EnergyUsedKWh is the pack-side energy: the file's own consumed energy when its header says so, else (start SoC - end SoC) × the pack
	// given in Options. EnergyEstimated marks the second case; zero with no pack.
	EnergyUsedKWh   float64
	EnergyEstimated bool
	RoutePolyline   string // precision 5; only for sources that carry points
	Source          string
	VehicleLabel    string // the file's own label for the vehicle, "" when none
}

// Charge is a parsed charge.
type Charge struct {
	StartedAt, EndedAt     time.Time
	StartSoCPct, EndSoCPct float64
	EnergyAddedKWh         float64
	// EnergyBasis is what EnergyAddedKWh measures, from the format: "battery", "wall" or "unknown". EnergyEstimated: it came from SoC × pack.
	EnergyBasis     string
	EnergyEstimated bool
	MaxPowerKW      float64
	AvgPowerKW      float64
	Lat, Lon        float64
	SessionType     string // "dc" when the peak power is at least 25 kW, else "ac"
	Cost            float64
	Currency        string
	HasCost         bool
	Source          string
	VehicleLabel    string
}

// ExternalID is the deterministic id of an imported session: the same physical session gives the same id on every import of any file of
// that source (design §3). kind is "d" for a drive, "c" for a charge.
func ExternalID(source, rivianVehicleID, kind string, start time.Time) string {
	prefix := source
	if len(prefix) > 7 && prefix[len(prefix)-7:] == "_import" {
		prefix = prefix[:len(prefix)-7]
	}
	return fmt.Sprintf("%s_%s_%s_%d", prefix, rivianVehicleID, kind, start.Unix())
}

const dcThresholdKW = 25
