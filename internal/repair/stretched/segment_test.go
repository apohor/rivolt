package stretched

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// parked builds n frames one minute apart from start, SoC rising linearly
// from soc0 to soc1, charging at kw (0 means not charging).
func parked(start time.Time, n int, soc0, soc1, kw float64) []Frame {
	out := make([]Frame, n)
	for i := range out {
		st := ""
		if kw > 0 {
			st = "charging_active"
		}
		out[i] = Frame{
			At: start.Add(time.Duration(i) * time.Minute), Gear: "P", ChargingState: st, PowerKW: kw,
			SoC: soc0 + (soc1-soc0)*float64(i)/float64(n-1),
		}
	}
	return out
}

func drive(start time.Time, n int) []Frame {
	out := make([]Frame, n)
	for i := range out {
		out[i] = Frame{At: start.Add(time.Duration(i) * time.Minute), Gear: "D", SpeedMph: 40, SoC: 60}
	}
	return out
}

func cat(parts ...[]Frame) []Frame {
	var out []Frame
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func end(f []Frame) time.Time { return f[len(f)-1].At.Add(time.Minute) }

func TestGradualRise(t *testing.T) {
	d := drive(t0, 5)
	// 120 minutes at 7.2 kW = 14.4 kWh; 20 points of a 72 kWh pack = 14.4 kWh.
	p := parked(end(d), 121, 40, 60, 7.2)
	segs := DetectSegments(cat(d, p), t0)
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	s := segs[0]
	if s.SoCStart != 40 || s.SoCEnd != 60 || !s.FirstActive.Equal(p[0].At) {
		t.Errorf("segment = %+v", s)
	}
	if math.Abs(s.IntegralKWh-14.4) > 0.05 {
		t.Errorf("integral = %.2f, want 14.4", s.IntegralKWh)
	}
	if got := s.SoCKWh(72); math.Abs(got-14.4) > 1e-9 {
		t.Errorf("SoCKWh = %.2f", got)
	}
	if !Agree(s.IntegralKWh, s.SoCKWh(72)) {
		t.Error("integral and SoC energy should agree")
	}
}

func TestTwoSegmentsInOneRow(t *testing.T) {
	d1 := drive(t0, 3)
	p1 := parked(end(d1), 61, 30, 45, 11)
	d2 := drive(end(p1), 3)
	p2 := parked(end(d2), 61, 45, 60, 11)
	segs := DetectSegments(cat(d1, p1, d2, p2), t0)
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2", len(segs))
	}
	if segs[0].SoCStart != 30 || segs[0].SoCEnd != 45 || segs[1].SoCStart != 45 || segs[1].SoCEnd != 60 {
		t.Errorf("segment SoC ranges = %+v %+v", segs[0], segs[1])
	}
	if !segs[0].End.Before(segs[1].Start) {
		t.Errorf("segments not ordered: %+v %+v", segs[0], segs[1])
	}
}

func TestDriveInTheMiddleSplitsOne(t *testing.T) {
	d := drive(t0, 3)
	a := parked(end(d), 30, 40, 50, 11)
	mid := drive(end(a), 2)
	b := parked(end(mid), 30, 50, 60, 11)
	segs := DetectSegments(cat(d, a, mid, b), t0)
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2 (a drive splits one)", len(segs))
	}
	for _, s := range segs {
		if s.SoCEnd-s.SoCStart < 5 {
			t.Errorf("segment under 5 points: %+v", s)
		}
	}
}

func TestSmallGainSegmentDropped(t *testing.T) {
	d := drive(t0, 3)
	// 4 points is under the threshold, even with power.
	if segs := DetectSegments(cat(d, parked(end(d), 30, 50, 54, 7)), t0); len(segs) != 0 {
		t.Fatalf("segments = %d, want 0", len(segs))
	}
}

func TestRiseWithoutActivePowerIsNotASegment(t *testing.T) {
	d := drive(t0, 3)
	p := parked(end(d), 30, 40, 60, 0) // SoC rises (e.g. a bad reading) but no charging_active frames
	if segs := DetectSegments(cat(d, p), t0); len(segs) != 0 {
		t.Fatalf("segments = %d, want 0", len(segs))
	}
	// charging_active with zero power does not count either.
	for i := range p {
		p[i].ChargingState = "charging_active"
	}
	if segs := DetectSegments(cat(d, p), t0); len(segs) != 0 {
		t.Fatalf("segments = %d, want 0 (power 0)", len(segs))
	}
}

func TestJitteryLowSpeedStaysParked(t *testing.T) {
	d := drive(t0, 3)
	p := parked(end(d), 61, 40, 60, 7.2)
	for i := range p {
		p[i].SpeedMph = 0.5 / 1.609 // 0.5 km/h of GPS jitter
	}
	if segs := DetectSegments(cat(d, p), t0); len(segs) != 1 {
		t.Fatalf("segments = %d, want 1 (jitter is not a drive)", len(segs))
	}
	// Over 2 mph in gear P still ends the segment.
	p[30].SpeedMph = 5
	if segs := DetectSegments(cat(d, p), t0); len(segs) != 2 {
		t.Fatalf("segments = %d, want 2 (a frame over 2 mph ends the first)", len(segs))
	}
}

func TestFramesBeforeFirstDriveIgnored(t *testing.T) {
	early := parked(t0, 30, 20, 40, 11)
	fd := end(early)
	d := drive(fd, 3)
	late := parked(end(d), 30, 60, 62, 0)
	if segs := DetectSegments(cat(early, d, late), fd); len(segs) != 0 {
		t.Fatalf("segments = %d, want 0 (the charge before the first drive is the row's own)", len(segs))
	}
}

func TestIntegralDoesNotBridgeGaps(t *testing.T) {
	d := drive(t0, 3)
	a := parked(end(d), 10, 40, 45, 10)
	// a 3 h hole in the data, then more charging.
	b := parked(a[len(a)-1].At.Add(3*time.Hour), 10, 50, 55, 10)
	segs := DetectSegments(cat(d, a, b), t0)
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	// 9 minutes of power in each half: 2 * (9/60) h * 10 kW = 3 kWh, not 3 h * 10 kW.
	if math.Abs(segs[0].IntegralKWh-3.0) > 0.05 {
		t.Errorf("integral = %.2f, want 3.0 (gap not integrated)", segs[0].IntegralKWh)
	}
}

func TestAgree(t *testing.T) {
	for _, c := range []struct {
		a, b float64
		want bool
	}{{10, 10, true}, {10, 12, true}, {10, 14, false}, {0, 10, false}, {10, 0, false}, {-1, 10, false}} {
		if got := Agree(c.a, c.b); got != c.want {
			t.Errorf("Agree(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
