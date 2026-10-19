package rivian

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/samples"
)

func parkedAt(t time.Time) samples.Sample {
	return samples.Sample{VehicleID: "v1", At: t, BatteryLevelPct: 80, Lat: 37.0, Lon: -122.0,
		ShiftState: "P", ChargingState: "charging_ready", ChargerStatus: "chrgr_sts_not_connected", PowerState: "ready"}
}

func TestParkedGateSkipsUnchangedRowsUntilHeartbeat(t *testing.T) {
	t.Parallel()
	var g parkedGate
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	wrote := 0
	for i := 0; i < 20*60/16; i++ { // 20 minutes of a row every 16 s
		if g.shouldWrite(parkedAt(t0.Add(time.Duration(i) * 16 * time.Second))) {
			wrote++
		}
	}
	// First row, then one per 5-minute heartbeat: 1 + 3 in 20 minutes.
	if wrote != 4 {
		t.Fatalf("wrote %d rows in 20 parked minutes, want 4", wrote)
	}
}

func TestParkedGateWritesEveryChange(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	for name, mut := range map[string]func(*samples.Sample){
		"soc step":     func(s *samples.Sample) { s.BatteryLevelPct = 79 },
		"gear":         func(s *samples.Sample) { s.ShiftState = "D"; s.SpeedMph = 3 },
		"plugged":      func(s *samples.Sample) { s.ChargerStatus = "chrgr_sts_connected_no_chrg" },
		"sleep":        func(s *samples.Sample) { s.PowerState = "sleep" },
		"charge limit": func(s *samples.Sample) { s.ChargeLimitPct = 90 },
		"moved 100 m":  func(s *samples.Sample) { s.Lat += 0.0009 },
		"fix lost":     func(s *samples.Sample) { s.Lat, s.Lon = 0, 0 },
		"drive opens":  func(s *samples.Sample) { s.DriveNumber = 1 },
		"charging":     func(s *samples.Sample) { s.ChargingState = "charging_active" },
	} {
		var g parkedGate
		g.shouldWrite(parkedAt(t0))
		s := parkedAt(t0.Add(16 * time.Second))
		mut(&s)
		if !g.shouldWrite(s) {
			t.Errorf("%s: change was skipped", name)
		}
	}
	// Small jitter is not a change.
	var g parkedGate
	g.shouldWrite(parkedAt(t0))
	s := parkedAt(t0.Add(16 * time.Second))
	s.BatteryLevelPct = 79.6
	s.Lat += 0.0001 // about 11 m
	if g.shouldWrite(s) {
		t.Error("sub-step jitter was written")
	}
}

func TestParkedGateNeverSkipsDrivingOrChargingRows(t *testing.T) {
	t.Parallel()
	var g parkedGate
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		d := parkedAt(t0.Add(time.Duration(i) * time.Second))
		d.ShiftState, d.SpeedMph, d.DriveNumber = "D", 40, 7
		if !g.shouldWrite(d) {
			t.Fatalf("driving row %d skipped", i)
		}
	}
	for i := 5; i < 10; i++ {
		c := parkedAt(t0.Add(time.Duration(i) * time.Second))
		c.ChargingState, c.ChargerPowerKW, c.ChargeNumber = "charging_active", 50, 3
		if !g.shouldWrite(c) {
			t.Fatalf("charging row %d skipped", i)
		}
	}
	// The first parked row after a drive is written (the state changed), and
	// then the unchanged ones are skipped.
	p := parkedAt(t0.Add(11 * time.Second))
	if !g.shouldWrite(p) {
		t.Fatal("first parked row after a drive skipped")
	}
	p.At = p.At.Add(16 * time.Second)
	if g.shouldWrite(p) {
		t.Fatal("repeat parked row written")
	}
}

func TestParkedGateIsPerVehicleAndForgets(t *testing.T) {
	t.Parallel()
	var g parkedGate
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	a, b := parkedAt(t0), parkedAt(t0)
	b.VehicleID = "v2"
	if !g.shouldWrite(a) || !g.shouldWrite(b) {
		t.Fatal("first row of each vehicle must be written")
	}
	a.At = a.At.Add(16 * time.Second)
	if g.shouldWrite(a) {
		t.Fatal("repeat written")
	}
	g.forget("v1")
	if !g.shouldWrite(a) {
		t.Fatal("after forget the next row must be written")
	}
}

func TestParkedDedupeSwitch(t *testing.T) {
	t.Setenv("RIVOLT_SAMPLE_PARKED_DEDUPE", "")
	if !parkedDedupeEnabled() {
		t.Error("default must be on")
	}
	t.Setenv("RIVOLT_SAMPLE_PARKED_DEDUPE", "false")
	if parkedDedupeEnabled() {
		t.Error("false must turn it off")
	}
	t.Setenv("RIVOLT_SAMPLE_PARKED_DEDUPE", "1")
	if !parkedDedupeEnabled() {
		t.Error("1 must be on")
	}
}

// On the generated truck (45 days: drives, charges, plugged and asleep parks)
// the gate drops most parked rows, keeps every driving and charging row, never
// leaves a longer gap than the heartbeat while parked, and the kept rows
// reproduce the state-of-charge series to within one step.
func TestParkedGateOnSampleTruck(t *testing.T) {
	t.Parallel()
	c := NewSampleCorpus(20261014)
	start, end := c.span()
	frames, err := c.Frames(context.Background(), start, end)
	if err != nil || len(frames) == 0 {
		t.Fatalf("frames: %d %v", len(frames), err)
	}
	var g parkedGate
	var kept []samples.Sample
	var all []samples.Sample
	parkedAll, parkedKept, active, activeKept := 0, 0, 0, 0
	for _, f := range frames {
		s := samples.Sample{VehicleID: "sample", At: f.At, BatteryLevelPct: f.BatteryLevelPct, Lat: f.Latitude, Lon: f.Longitude,
			SpeedMph: f.SpeedKph * kphToMi, ShiftState: f.Gear, DriveMode: f.DriveMode, ChargingState: f.ChargerState,
			ChargerStatus: f.ChargerStatus, PowerState: f.PowerState, ChargeLimitPct: f.ChargeTargetPct}
		if isDrivingGear(f.Gear) {
			s.DriveNumber = 1
		}
		if isChargingCS(f.ChargerState) {
			s.ChargeNumber = 1
		}
		all = append(all, s)
		w := g.shouldWrite(s)
		if w {
			kept = append(kept, s)
		}
		if isParkedSample(s) {
			parkedAll++
			if w {
				parkedKept++
			}
		} else {
			active++
			if w {
				activeKept++
			}
		}
	}
	if activeKept != active {
		t.Fatalf("driving/charging rows kept %d of %d", activeKept, active)
	}
	t.Logf("frames %d, kept %d (%.0f%% saved); parked %d -> %d", len(all), len(kept), 100*(1-float64(len(kept))/float64(len(all))), parkedAll, parkedKept)
	if parkedKept*2 > parkedAll {
		t.Errorf("parked rows only fell from %d to %d", parkedAll, parkedKept)
	}
	// Replay the kept rows as a step series and compare it with every frame.
	k := 0
	for _, s := range all {
		for k+1 < len(kept) && !kept[k+1].At.After(s.At) {
			k++
		}
		if d := math.Abs(kept[k].BatteryLevelPct - s.BatteryLevelPct); d >= parkedSoCStep+0.001 {
			t.Fatalf("at %s the kept series says %.1f%%, the truck was at %.1f%%", s.At, kept[k].BatteryLevelPct, s.BatteryLevelPct)
		}
		if s.At.Sub(kept[k].At) > parkedHeartbeat+4*time.Minute { // asleep frames come every 3 minutes
			t.Fatalf("no row for %s before %s", s.At.Sub(kept[k].At), s.At)
		}
	}
}
