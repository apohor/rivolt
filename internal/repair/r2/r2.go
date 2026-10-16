// Package r2 rebuilds the drives and charges of a vehicle whose
// vehicleState gear was unusable (R2 before #214: the gear stayed at
// the last D/R for days, so drives never closed and no charge opened).
// It re-segments from vehicle_state rows: an R2 reports power "go"
// while driving and "ready" otherwise, which is reliable even when its
// gear isn't. Plan: scratchpad/r2-repair-plan.md.
package r2

import (
	"fmt"
	"time"
)

// Row is one vehicle_state sample.
type Row struct {
	At            time.Time
	Power         string // power_state; "" = not reported
	ChargingState string
	OdoMi         float64
	SoC           float64
	SpeedMph      float64
	Lat, Lon      float64
	KW            float64 // charger_power_kw
}

// Segmentation limits.
const (
	// TripBreak: power not "go" for this long ends a trip.
	TripBreak = 2 * time.Minute
	// TripMinMi: a "go" run that moved less is preconditioning or
	// sitting in drive, not a trip.
	TripMinMi = 0.1
	// ChargeBreak: a gap this long between charging_active rows ends a
	// charge (the recorder's liveChargeMaxGap).
	ChargeBreak = 30 * time.Minute
	// ChargeGapCap caps each row's share of the power integral (the
	// phantom audit's rule) so a telemetry gap can't invent energy.
	ChargeGapCap = 10 * time.Minute
	// ChargeMinKWh: below this a charging_active run is noise.
	ChargeMinKWh = 1.0
)

// Trip is a re-segmented drive.
type Trip struct {
	Start, End           time.Time
	StartOdoMi, EndOdoMi float64
	StartSoC, EndSoC     float64
	StartLat, StartLon   float64
	EndLat, EndLon       float64
	MaxSpeedMph          float64
	Path                 [][2]float64 // lat, lon
}

// DistanceMi is the odometer distance.
func (t Trip) DistanceMi() float64 { return t.EndOdoMi - t.StartOdoMi }

// AvgSpeedMph is distance over wall time.
func (t Trip) AvgSpeedMph() float64 {
	h := t.End.Sub(t.Start).Hours()
	if h <= 0 {
		return 0
	}
	return t.DistanceMi() / h
}

// EnergyKWh is the SoC used times the pack (0 when SoC rose).
func (t Trip) EnergyKWh(packKWh float64) float64 {
	if d := t.StartSoC - t.EndSoC; d > 0 && packKWh > 0 {
		return d / 100 * packKWh
	}
	return 0
}

// Charge is a recovered charging session.
type Charge struct {
	Start, End       time.Time
	StartSoC, EndSoC float64
	EnergyKWh        float64 // power integral
	PeakKW           float64
	Lat, Lon         float64
	ActiveSeconds    float64
}

// Segment splits time-ordered rows into trips and charges. A row with
// power not reported ("") continues whatever state came before it: an
// R2 leaves power blank in about a fifth of its rows, mostly around
// resubscribes, and the odometer moving across them is still driving.
func Segment(rows []Row) ([]Trip, []Charge) {
	return trips(rows), charges(rows)
}

func trips(rows []Row) []Trip {
	var out []Trip
	var cur *Trip
	var lastGo time.Time
	going, stopped := false, false
	flush := func() {
		if cur != nil && cur.DistanceMi() >= TripMinMi {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, r := range rows {
		switch r.Power {
		case "go":
			going = true
		case "":
			// continues the previous state
		default:
			going = false
		}
		if !going {
			if cur != nil {
				stopped = true
				if r.At.Sub(lastGo) >= TripBreak {
					flush()
				}
			}
			continue
		}
		// Stopped (a non-go row) and silent until this go row, long
		// enough: that was a park, not a pause.
		if cur != nil && stopped && r.At.Sub(lastGo) >= TripBreak {
			flush()
		}
		stopped = false
		lastGo = r.At
		if cur == nil {
			cur = &Trip{Start: r.At, StartOdoMi: r.OdoMi, StartSoC: r.SoC, StartLat: r.Lat, StartLon: r.Lon}
		}
		// Extend: end state follows the last driving row; the odometer
		// and SoC only ever take a real reading.
		cur.End = r.At
		if r.OdoMi > cur.EndOdoMi {
			cur.EndOdoMi = r.OdoMi
		}
		if cur.StartOdoMi == 0 && r.OdoMi > 0 {
			cur.StartOdoMi = r.OdoMi
		}
		if r.SoC > 0 {
			if cur.StartSoC == 0 {
				cur.StartSoC = r.SoC
			}
			cur.EndSoC = r.SoC
		}
		if r.Lat != 0 || r.Lon != 0 {
			if cur.StartLat == 0 && cur.StartLon == 0 {
				cur.StartLat, cur.StartLon = r.Lat, r.Lon
			}
			cur.EndLat, cur.EndLon = r.Lat, r.Lon
			if n := len(cur.Path); n == 0 || cur.Path[n-1] != [2]float64{r.Lat, r.Lon} {
				cur.Path = append(cur.Path, [2]float64{r.Lat, r.Lon})
			}
		}
		if r.SpeedMph > cur.MaxSpeedMph {
			cur.MaxSpeedMph = r.SpeedMph
		}
	}
	flush()
	return out
}

func charges(rows []Row) []Charge {
	var out []Charge
	var cur *Charge
	var last time.Time
	flush := func() {
		if cur != nil && cur.EnergyKWh >= ChargeMinKWh {
			out = append(out, *cur)
		}
		cur = nil
	}
	var prev *Row
	for i := range rows {
		r := rows[i]
		if r.ChargingState != "charging_active" {
			continue
		}
		if cur != nil && r.At.Sub(last) > ChargeBreak {
			flush()
			prev = nil
		}
		if cur == nil {
			cur = &Charge{Start: r.At, StartSoC: r.SoC, Lat: r.Lat, Lon: r.Lon}
		}
		// Power integral: each row's power held until the next
		// charging_active row, capped.
		if prev != nil {
			dt := r.At.Sub(prev.At)
			if dt > ChargeGapCap {
				dt = ChargeGapCap
			}
			cur.EnergyKWh += prev.KW * dt.Hours()
			if prev.KW > 0 {
				cur.ActiveSeconds += dt.Seconds()
			}
		}
		cur.End = r.At
		if r.SoC > 0 {
			if cur.StartSoC == 0 {
				cur.StartSoC = r.SoC
			}
			cur.EndSoC = r.SoC
		}
		if r.KW > cur.PeakKW {
			cur.PeakKW = r.KW
		}
		if cur.Lat == 0 && cur.Lon == 0 && (r.Lat != 0 || r.Lon != 0) {
			cur.Lat, cur.Lon = r.Lat, r.Lon
		}
		last = r.At
		prev = &rows[i]
	}
	flush()
	return out
}

// ExternalID is the recorder's id for a live session started at t
// (liveSessionID): re-running the repair, or the recorder re-saving,
// collapses onto the same row.
func ExternalID(rivianVehicleID, kind string, t time.Time) string {
	return fmt.Sprintf("live_%s_%s_%d", rivianVehicleID, kind, t.UTC().Unix())
}
