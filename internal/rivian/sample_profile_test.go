package rivian

import (
	"math"
	"reflect"
	"sort"
	"testing"
	"time"
)

// driveFrames returns each drive segment's frames.
func driveFrames(c *SampleCorpus) (segs []sampleSeg, frames [][]replayFrame) {
	for _, s := range c.segs {
		if s.kind != segDrive {
			continue
		}
		fs := make([]replayFrame, s.frames())
		for i := range fs {
			fs[i] = s.frameAt(i)
		}
		segs = append(segs, s)
		frames = append(frames, fs)
	}
	return
}

func meanStd(xs []float64) (m, sd float64) {
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	for _, x := range xs {
		sd += (x - m) * (x - m)
	}
	return m, math.Sqrt(sd / float64(len(xs)))
}

// stops counts runs of >= 4 consecutive zero-speed frames (>= 20 s).
func stops(fs []replayFrame) int {
	n, run := 0, 0
	for _, f := range fs {
		if f.SpeedMph == 0 {
			run++
			if run == 4 {
				n++
			}
		} else {
			run = 0
		}
	}
	return n
}

func TestDriveProfileRealisticAndPreservesTotals(t *testing.T) {
	c := NewSampleCorpus(SampleSeed)
	segs, all := driveFrames(c)
	if len(segs) < 50 {
		t.Fatalf("only %d drives", len(segs))
	}
	var cityLong, hwyLong, sport, upticks int
	for k, s := range segs {
		fs := all[k]
		if s.prof == nil {
			t.Fatalf("drive %d has no profile", k)
		}
		// Totals: distance (integral of speed), odometer and SoC ends.
		miles := s.odoB - s.odoA
		dt := s.step.Hours()
		var integ float64
		for i, f := range fs {
			w := 1.0
			if i == len(fs)-1 {
				if rem := s.end.Sub(s.start) - time.Duration(len(fs)-1)*s.step; rem > 0 {
					w = float64(rem) / float64(s.step)
				}
			}
			integ += f.SpeedMph * dt * w
		}
		if math.Abs(integ-miles) > 0.01*miles+0.05 {
			t.Errorf("drive %d: integral of speed %.2f mi, plan %.2f mi", k, integ, miles)
		}
		if got := fs[0].OdometerMi; math.Abs(got-round2(s.odoA)) > 0.011 {
			t.Errorf("drive %d: starts at odo %.2f, plan %.2f", k, got, s.odoA)
		}
		if got := fs[0].BatteryPct; math.Abs(got-s.socA) > 0.06 {
			t.Errorf("drive %d: starts at SoC %.1f, plan %.1f", k, got, s.socA)
		}
		if got := fs[len(fs)-1].BatteryPct; math.Abs(got-s.socB) > 0.35 {
			t.Errorf("drive %d: ends at SoC %.1f, plan %.1f", k, got, s.socB)
		}
		if math.Abs(s.prof.scale-1) > 0.15 {
			t.Errorf("drive %d: speeds scaled by %.2f to hit the plan's distance", k, s.prof.scale)
		}
		var sp []float64
		var prevOdo float64
		mode := "everyday"
		for i, f := range fs {
			sp = append(sp, f.SpeedMph)
			if f.SpeedMph < 0 || f.SpeedMph > 125 {
				t.Fatalf("drive %d frame %d: speed %.1f", k, i, f.SpeedMph)
			}
			if f.OdometerMi < prevOdo-1e-9 {
				t.Fatalf("drive %d: odometer went backwards at %d", k, i)
			}
			prevOdo = f.OdometerMi
			if mode == "sport" && f.DriveMode != "sport" {
				t.Fatalf("drive %d: mode flipped back", k)
			}
			mode = f.DriveMode
			if i > 0 && f.BatteryPct > fs[i-1].BatteryPct {
				upticks++
			}
		}
		if mode == "sport" {
			sport++
		}
		if first, last := fs[0].SpeedMph, fs[len(fs)-1].SpeedMph; first > 15 || last > 15 {
			t.Errorf("drive %d: starts at %.1f mph and ends at %.1f; should leave and arrive at rest", k, first, last)
		}
		dur := s.end.Sub(s.start)
		_, sd := meanStd(sp)
		switch {
		case s.cruise < 45 && dur >= 20*time.Minute:
			cityLong++
			if n := stops(fs); n < 2 {
				t.Errorf("city drive %d (%v): %d stops, want stop-and-go", k, dur, n)
			}
			if mx := maxOf(sp); mx > 45 {
				t.Errorf("city drive %d: top speed %.1f mph", k, mx)
			}
			if sd < 8 {
				t.Errorf("city drive %d: speed std %.1f, want stop-and-go variance", k, sd)
			}
		case s.cruise >= 45 && dur >= 30*time.Minute:
			hwyLong++
			if mx := maxOf(sp); mx > 80 {
				t.Errorf("highway drive %d: top speed %.1f mph", k, mx)
			}
			if sd < 1 || sd > 22 {
				t.Errorf("highway drive %d: speed std %.1f out of bounds", k, sd)
			}
		}
	}
	var cityMoving []float64
	for k, s := range segs {
		if s.cruise >= 45 {
			continue
		}
		for _, f := range all[k] {
			if f.SpeedMph > 0 {
				cityMoving = append(cityMoving, f.SpeedMph)
			}
		}
	}
	sort.Float64s(cityMoving)
	if p90 := cityMoving[len(cityMoving)*9/10]; p90 > 45 {
		t.Errorf("city p90 moving speed %.1f mph, want <= 45", p90)
	}
	if cityLong < 10 || hwyLong < 3 {
		t.Fatalf("corpus too thin to judge: %d city, %d highway long drives", cityLong, hwyLong)
	}
	if share := float64(sport) / float64(len(segs)); sport == 0 || share > 0.25 {
		t.Errorf("%d of %d drives switch mode; want about one in ten", sport, len(segs))
	}
	if upticks == 0 {
		t.Error("SoC never ticks up under regen")
	}
}

// The speed is not a periodic function: two windows of one long drive
// are not copies of each other, and the series has no dominant period.
func TestDriveProfileNotPeriodic(t *testing.T) {
	c := NewSampleCorpus(SampleSeed)
	segs, all := driveFrames(c)
	checked := 0
	for k, s := range segs {
		if s.end.Sub(s.start) < 40*time.Minute {
			continue
		}
		sp := make([]float64, len(all[k]))
		for i, f := range all[k] {
			sp[i] = f.SpeedMph
		}
		m, sd := meanStd(sp)
		if sd == 0 {
			t.Fatalf("drive %d: constant speed", k)
		}
		// Normalised autocorrelation at every lag from 10 s to 15 min:
		// a sine of any period would reach ~1 at its period.
		for lag := 2; lag < 180 && lag < len(sp)/2; lag++ {
			var num float64
			for i := 0; i+lag < len(sp); i++ {
				num += (sp[i] - m) * (sp[i+lag] - m)
			}
			r := num / (float64(len(sp)-lag) * sd * sd)
			if r > 0.97 {
				t.Errorf("drive %d: autocorrelation %.2f at lag %d: periodic", k, r, lag)
			}
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("only %d long drives checked", checked)
	}
}

func TestDriveProfileDeterministic(t *testing.T) {
	a, fa := driveFrames(NewSampleCorpus(SampleSeed))
	b, fb := driveFrames(NewSampleCorpus(SampleSeed))
	if len(a) != len(b) {
		t.Fatal("drive count differs")
	}
	for k := range fa {
		for i := range fa[k] {
			if !reflect.DeepEqual(fa[k][i], fb[k][i]) {
				t.Fatalf("drive %d frame %d differs between builds", k, i)
			}
		}
	}
}

func maxOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = math.Max(m, x)
	}
	return m
}
