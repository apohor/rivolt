package stretched

import (
	"strings"
	"time"
)

// Segment detection for the split path: a stretched charge row can hold a real
// charge that happened while the car sat parked between its drives (the
// absorbed charge of #1619/#1620). DetectSegments finds those parked stretches
// from the row's own frames, with no database, so the CLI and its tests share
// one definition.

const (
	// segmentDriveSpeedMph mirrors the recorder's driveCloseSpeedMph: at or
	// under it the car counts as standing still (GPS speed jitter, a 0.5 km/h
	// creep). Over it, or in D/R/N, the car is driving and ends a segment.
	segmentDriveSpeedMph = 2.0
	// segmentMinGainPct is the net SoC gain that makes a parked stretch a charge.
	segmentMinGainPct = 5.0
	// segmentMaxGap caps the time between two frames that the energy integral
	// bridges, so a hole in the data is not integrated as if power held.
	segmentMaxGap = 10 * time.Minute
	// agreeTolerance is how far the power integral and the SoC-times-pack energy
	// may differ, as a fraction of the larger.
	agreeTolerance = 0.25
)

// Frame is one vehicle_state sample of the row, ascending by At.
type Frame struct {
	At            time.Time
	SpeedMph      float64
	PowerKW       float64
	SoC           float64 // percent
	Gear          string
	ChargingState string
}

// Segment is a parked stretch that holds a charge.
type Segment struct {
	Start, End  time.Time // first and last parked frame of the stretch
	FirstActive time.Time // first charging_active frame with power > 0
	SoCStart    float64
	SoCEnd      float64
	IntegralKWh float64 // trapezoidal power x time over consecutive active frames
}

func (f Frame) driving() bool {
	switch strings.ToUpper(strings.TrimSpace(f.Gear)) {
	case "D", "R", "N":
		return true
	}
	return f.SpeedMph > segmentDriveSpeedMph
}

func (f Frame) active() bool {
	return strings.EqualFold(strings.TrimSpace(f.ChargingState), "charging_active") && f.PowerKW > 0
}

// DetectSegments returns the parked segments after firstDrive that gained at
// least 5 SoC points and have charging_active frames at power > 0. A drive
// (gear D/R/N, or speed over 2 mph) ends a segment, so one row can yield
// several. Frames before firstDrive are ignored.
func DetectSegments(frames []Frame, firstDrive time.Time) []Segment {
	var out []Segment
	var run []Frame
	flush := func() {
		if s, ok := segmentOf(run); ok {
			out = append(out, s)
		}
		run = run[:0]
	}
	for _, f := range frames {
		if f.At.Before(firstDrive) {
			continue
		}
		if f.driving() {
			flush()
			continue
		}
		run = append(run, f)
	}
	flush()
	return out
}

func segmentOf(run []Frame) (Segment, bool) {
	if len(run) < 2 {
		return Segment{}, false
	}
	s := Segment{
		Start:    run[0].At,
		End:      run[len(run)-1].At,
		SoCStart: run[0].SoC,
		SoCEnd:   run[len(run)-1].SoC,
	}
	if s.SoCEnd-s.SoCStart < segmentMinGainPct {
		return Segment{}, false
	}
	for i, f := range run {
		if !f.active() {
			continue
		}
		if s.FirstActive.IsZero() {
			s.FirstActive = f.At
		}
		if i == 0 || !run[i-1].active() {
			continue
		}
		dt := f.At.Sub(run[i-1].At)
		if dt <= 0 || dt > segmentMaxGap {
			continue
		}
		s.IntegralKWh += (f.PowerKW + run[i-1].PowerKW) / 2 * dt.Hours()
	}
	if s.FirstActive.IsZero() {
		return Segment{}, false
	}
	return s, true
}

// SoCKWh is the energy the SoC gain implies for a pack of packKWh.
func (s Segment) SoCKWh(packKWh float64) float64 {
	return (s.SoCEnd - s.SoCStart) / 100 * packKWh
}

// Agree reports whether the power integral and the SoC-derived energy are
// within 25% of each other (of the larger). Both must be positive.
func Agree(integralKWh, socKWh float64) bool {
	if integralKWh <= 0 || socKWh <= 0 {
		return false
	}
	hi, lo := integralKWh, socKWh
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi-lo)/hi <= agreeTolerance
}
