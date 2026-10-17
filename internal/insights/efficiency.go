// Package insights computes read-only breakdowns of the user's own
// recorded data. Efficiency: miles per kWh bucketed by outside
// temperature, average speed and drive mode over a period.
package insights

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/recap"
)

// DriveInput is one recorded drive with the conditions it was driven in.
// OutsideTempC is the median non-zero outside temperature over the
// drive's samples (nil without any); DriveMode the mode it spent the most
// samples in, as the vehicle reports it ("everyday", "distance", ...).
type DriveInput struct {
	ID           string
	VehicleID    string
	StartedAt    time.Time
	EndedAt      time.Time
	CreatedAt    time.Time
	Source       string
	DistanceMi   float64
	EnergyKWh    float64
	AvgSpeedMph  float64
	OutsideTempC *float64
	DriveMode    string
}

// Units picks the distance, speed and temperature units of the labels
// and figures.
type Units string

const (
	Imperial Units = "imperial"
	Metric   Units = "metric"
)

// MinBucketMiles is the distance below which a bucket's efficiency is
// flagged not_enough_data: a few short drives, each quantized to a
// percent of SoC, don't make a figure. The UI decides how to show it.
const MinBucketMiles = 25.0

// A drive whose own figure is outside this range is a recording glitch
// (a missed SoC sample, a stub), not a way of driving. Rivians run about
// 1.5–3.5 mi/kWh.
const (
	minPlausibleMiPerKWh = 0.3
	maxPlausibleMiPerKWh = 10
)

const kmPerMi = 1.609344

// Bucket is one row of a breakdown. Efficiency is distance over energy
// across the bucket's drives (so weighted by distance, not by drive): nil
// without energy.
type Bucket struct {
	Key           string   `json:"key"`
	Label         string   `json:"label"`
	Distance      float64  `json:"distance"`
	EnergyKWh     float64  `json:"energy_kwh"`
	Efficiency    *float64 `json:"efficiency"`
	Drives        int      `json:"drives"`
	NotEnoughData bool     `json:"not_enough_data"`
}

// Efficiency is the response of /api/insights/efficiency.
type Efficiency struct {
	Units          Units  `json:"units"`
	DistanceUnit   string `json:"distance_unit"`   // "mi" | "km"
	EfficiencyUnit string `json:"efficiency_unit"` // "mi/kWh" | "km/kWh"
	// MinBucketDistance is MinBucketMiles in DistanceUnit.
	MinBucketDistance float64 `json:"min_bucket_distance"`
	Total             Bucket  `json:"total"`
	// ByTemperature has a row per band, coldest first. Drives without an
	// outside temperature are left out and counted in Excluded.
	//
	// Later: energy_edge_compute.graphs.cold_weather_soc (Parallax, the
	// #343 capture; scratchpad parallax-next.md) carries the vehicle's own
	// share of SoC lost to cold and its range impact. Once promoted it
	// can sit beside these bands as Rivian's figure for the cold buckets.
	ByTemperature []Bucket `json:"by_temperature"`
	// BySpeed has a row per average-speed band, slowest first.
	BySpeed []Bucket `json:"by_speed"`
	// ByDriveMode has a row per mode, most distance first. Drives with no
	// recorded mode are left out and counted in Excluded.
	ByDriveMode []Bucket `json:"by_drive_mode"`
	Excluded    Excluded `json:"excluded"`
}

// Excluded counts the drives in the period left out of the breakdowns.
type Excluded struct {
	// NoEnergy: no energy recorded, or a figure outside the plausible
	// range; left out of every breakdown and the total.
	NoEnergy int `json:"no_energy"`
	// Duplicates: overlapping duplicate recordings (the Money ledger's
	// rule); the kept recording counts.
	Duplicates int `json:"duplicates"`
	// NoTemperature / NoDriveMode: in the total, not in that breakdown.
	NoTemperature int `json:"no_temperature"`
	NoDriveMode   int `json:"no_drive_mode"`
}

type band struct {
	key        string
	lo, hi     float64 // [lo, hi); ±Inf open ends
	metricText string
	imperText  string
}

// Temperature bands in °C, labelled in °F for imperial.
var tempBands = []band{
	{"below_0c", math.Inf(-1), 0, "Below 0 °C", "Below 32 °F"},
	{"0_10c", 0, 10, "0–10 °C", "32–50 °F"},
	{"10_20c", 10, 20, "10–20 °C", "50–68 °F"},
	{"20_30c", 20, 30, "20–30 °C", "68–86 °F"},
	{"30c_up", 30, math.Inf(1), "30 °C and above", "86 °F and above"},
}

// Average-speed bands in mph, labelled in km/h for metric.
var speedBands = []band{
	{"city", 0, 35, "City (under 56 km/h)", "City (under 35 mph)"},
	{"mixed", 35, 55, "Mixed (56–89 km/h)", "Mixed (35–55 mph)"},
	{"highway", 55, 70, "Highway (89–113 km/h)", "Highway (55–70 mph)"},
	{"fast_highway", 70, math.Inf(1), "Fast highway (113 km/h and above)", "Fast highway (70 mph and above)"},
}

type acc struct {
	miles, kwh float64
	drives     int
}

func (a *acc) add(d DriveInput) {
	a.miles += d.DistanceMi
	a.kwh += d.EnergyKWh
	a.drives++
}

func (a acc) bucket(key, label string, u Units) Bucket {
	b := Bucket{Key: key, Label: label, Distance: round(dist(a.miles, u), 1), EnergyKWh: round(a.kwh, 1),
		Drives: a.drives, NotEnoughData: a.miles < MinBucketMiles}
	if a.kwh > 0 {
		e := round(dist(a.miles, u)/a.kwh, 2)
		b.Efficiency = &e
	}
	return b
}

// BuildEfficiency computes the breakdowns over ds (the period's drives).
func BuildEfficiency(ds []DriveInput, u Units) Efficiency {
	if u != Metric {
		u = Imperial
	}
	out := Efficiency{Units: u, DistanceUnit: "mi", EfficiencyUnit: "mi/kWh", MinBucketDistance: MinBucketMiles}
	if u == Metric {
		out.DistanceUnit, out.EfficiencyUnit = "km", "km/kWh"
		out.MinBucketDistance = round(MinBucketMiles*kmPerMi, 0)
	}
	kept, dups := dedupe(ds)
	out.Excluded.Duplicates = dups

	var total acc
	temps := make([]acc, len(tempBands))
	speeds := make([]acc, len(speedBands))
	modes := map[string]*acc{}
	for _, d := range kept {
		if !plausible(d) {
			out.Excluded.NoEnergy++
			continue
		}
		total.add(d)
		if d.OutsideTempC != nil {
			temps[bandOf(tempBands, *d.OutsideTempC)].add(d)
		} else {
			out.Excluded.NoTemperature++
		}
		speeds[bandOf(speedBands, avgSpeedMph(d))].add(d)
		if m := strings.ToLower(strings.TrimSpace(d.DriveMode)); m != "" {
			if modes[m] == nil {
				modes[m] = &acc{}
			}
			modes[m].add(d)
		} else {
			out.Excluded.NoDriveMode++
		}
	}
	out.Total = total.bucket("total", "All drives", u)
	out.ByTemperature = rows(tempBands, temps, u)
	out.BySpeed = rows(speedBands, speeds, u)
	out.ByDriveMode = []Bucket{}
	for m, a := range modes {
		out.ByDriveMode = append(out.ByDriveMode, a.bucket(m, recap.HumanizeDriveMode(m), u))
	}
	sort.Slice(out.ByDriveMode, func(i, j int) bool {
		if out.ByDriveMode[i].Distance != out.ByDriveMode[j].Distance {
			return out.ByDriveMode[i].Distance > out.ByDriveMode[j].Distance
		}
		return out.ByDriveMode[i].Key < out.ByDriveMode[j].Key
	})
	return out
}

// rows lists every band, empty ones included, so the UI's rows don't
// shift between periods.
func rows(bands []band, accs []acc, u Units) []Bucket {
	out := make([]Bucket, len(bands))
	for i, b := range bands {
		label := b.imperText
		if u == Metric {
			label = b.metricText
		}
		out[i] = accs[i].bucket(b.key, label, u)
	}
	return out
}

func bandOf(bands []band, v float64) int {
	for i, b := range bands {
		if v >= b.lo && v < b.hi {
			return i
		}
	}
	return len(bands) - 1
}

// dedupe drops overlapping duplicate recordings with the ledger's rule,
// so the drive counts agree with Money.
func dedupe(ds []DriveInput) ([]DriveInput, int) {
	byID := make(map[string]DriveInput, len(ds))
	ld := make([]ledger.Drive, 0, len(ds))
	for _, d := range ds {
		byID[d.ID] = d
		dist, kwh := d.DistanceMi, d.EnergyKWh
		ld = append(ld, ledger.Drive{ID: d.ID, VehicleID: d.VehicleID, StartedAt: d.StartedAt, EndedAt: d.EndedAt,
			DistanceMi: &dist, EnergyKWh: &kwh, Source: d.Source, CreatedAt: d.CreatedAt})
	}
	kept, lost := ledger.EffectiveDrives(ld)
	out := make([]DriveInput, 0, len(kept))
	for _, k := range kept {
		out = append(out, byID[k.ID])
	}
	return out, len(lost)
}

func plausible(d DriveInput) bool {
	if d.DistanceMi <= 0 || d.EnergyKWh <= 0 {
		return false
	}
	e := d.DistanceMi / d.EnergyKWh
	return e >= minPlausibleMiPerKWh && e <= maxPlausibleMiPerKWh
}

// avgSpeedMph is the recorded average speed, or distance over the drive's
// duration when none was recorded.
func avgSpeedMph(d DriveInput) float64 {
	if d.AvgSpeedMph > 0 {
		return d.AvgSpeedMph
	}
	if h := d.EndedAt.Sub(d.StartedAt).Hours(); h > 0 {
		return d.DistanceMi / h
	}
	return 0
}

func dist(mi float64, u Units) float64 {
	if u == Metric {
		return mi * kmPerMi
	}
	return mi
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
