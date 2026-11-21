package rivian

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func segOf(p simPlan, k sampleSegKind) sampleSeg {
	for _, s := range p.segs {
		if s.kind == k {
			return s
		}
	}
	return sampleSeg{}
}

// The short drive is a nearer errand at the full drive's cruise speed: distance, time and energy keep the full drive's ratios.
func TestShortDriveKeepsTheFullDrivesPhysics(t *testing.T) {
	start := simStartFor(t, SimDriveShort)
	short, err := planSim(SimDriveShort, start)
	if err != nil {
		t.Fatal(err)
	}
	full, err := planSim(SimDrive, simStartFor(t, SimDrive))
	if err != nil {
		t.Fatal(err)
	}
	ds, df := segOf(short, segDrive), segOf(full, segDrive)
	if ds.cruise != df.cruise {
		t.Fatalf("cruise %.2f, want the full drive's %.2f", ds.cruise, df.cruise)
	}
	for name, s := range map[string]sampleSeg{"short": ds, "full": df} {
		miles, hours := s.odoB-s.odoA, s.end.Sub(s.start).Hours()
		if v := miles / hours; math.Abs(v-s.cruise)/s.cruise > 0.02 {
			t.Errorf("%s: distance over time is %.1f mph, cruise is %.1f", name, v, s.cruise)
		}
	}
	perMi := func(s sampleSeg) float64 { return (s.socA - s.socB) / (s.odoB - s.odoA) }
	if a, b := perMi(ds), perMi(df); math.Abs(a-b)/b > 0.01 {
		t.Errorf("energy per mile %.4f vs the full drive's %.4f", a, b)
	}
	if m := ds.end.Sub(ds.start); m < 4*time.Minute || m > 5*time.Minute+30*time.Second {
		t.Errorf("short drive is %s of driving, want about 5 minutes", m)
	}
	if mi := ds.odoB - ds.odoA; mi < 3 {
		t.Errorf("short drive is %.1f mi, too short to show start, middle and end", mi)
	}
	if first, last := short.segs[0], short.segs[len(short.segs)-1]; first.kind != segParked || last.kind != segParked || first.end.Sub(first.start) != 30*time.Second || last.end.Sub(last.start) != 30*time.Second {
		t.Errorf("shape: want 30 s parked, the drive, 30 s parked")
	}
}

// The short charge is a DC fast slice: SoC rises 5 points or more in about four minutes, the energy is the power curve's integral, and the
// recorder sees the same fast-charge frames (pack temperature up) as at the corpus's own fast stops.
func TestShortChargeIsADCFastSlice(t *testing.T) {
	start := simStartFor(t, SimChargeShort)
	p, err := planSim(SimChargeShort, start)
	if err != nil {
		t.Fatal(err)
	}
	c := segOf(p, segCharge)
	if !c.dcfc || c.peakKW != simFastKW {
		t.Fatalf("charge dcfc=%v peak=%.0f kW, want a DC fast slice at %.0f", c.dcfc, c.peakKW, simFastKW)
	}
	if gain := c.socB - c.socA; gain < 5 {
		t.Errorf("SoC rises %.2f points, want 5 or more", gain)
	}
	if m := c.end.Sub(c.start).Minutes(); m < 3 || m > 4.5 {
		t.Errorf("charging slice %.2f min, want 3 to 4.5", m)
	}
	// Integrate the frames' power: it must equal the energy the SoC rise stands for, within 6 % (the 30 kW floor trims the last seconds).
	var kwh, peak, tempMax float64
	for i := 0; i < c.frames(); i++ {
		fr := c.frameAt(i)
		kwh += fr.ChargerKW * c.step.Hours()
		peak = math.Max(peak, fr.ChargerKW)
		tempMax = math.Max(tempMax, fr.PackTempMaxC)
	}
	if want := (c.socB - c.socA) / 100 * samplePackKWh; math.Abs(kwh-want)/want > 0.06 {
		t.Errorf("power integrates to %.2f kWh, the SoC rise is %.2f kWh", kwh, want)
	}
	if peak < 140 || peak > 175 {
		t.Errorf("peak power %.0f kW, want about 150", peak)
	}
	if tempMax <= 30 {
		t.Errorf("pack temperature %.0f, want the fast-charge reading", tempMax)
	}
	if last := p.segs[len(p.segs)-1]; last.kind != segParked || last.plugged {
		t.Errorf("the run should end parked and unplugged, got %+v", last.kind)
	}
	if d := p.end.Sub(start); d > 6*time.Minute {
		t.Errorf("short charge run lasts %s, want about 5 minutes", d)
	}
}

// The pair, back to back with queue, fits the gate's window.
func TestShortPairFitsElevenMinutes(t *testing.T) {
	ctx := context.Background()
	_, tl := sampleShared()
	var start time.Time
	for d := time.Duration(0); d < tl.span(); d += 7 * time.Minute {
		at := tl.loopStart(100).Add(d)
		p, err := planSim(SimDriveShort, at)
		if err != nil {
			continue
		}
		if _, err := planSim(SimChargeShort, p.end); err == nil {
			start = at
			break
		}
	}
	if start.IsZero() {
		t.Skip("the sample day has no parked stretch long enough for a drive then a charge")
	}
	ctl := NewSimControl(&memSimStore{})
	now := start.Add(-simLead - time.Second)
	ctl.now = func() time.Time { return now }
	d, err := ctl.Start(ctx, SimDrive, SimOpts{Short: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != SimDriveShort {
		t.Fatalf("kind %s", d.Kind)
	}
	if _, err := ctl.Start(ctx, SimCharge, SimOpts{Short: true}); !errors.Is(err, ErrSimBusy) {
		t.Fatalf("without queue while one runs: %v", err)
	}
	c, err := ctl.Start(ctx, SimCharge, SimOpts{Short: true, Queue: true})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Start.Equal(d.End) || c.Kind != SimChargeShort {
		t.Errorf("queued run %+v does not begin at the drive's end %s", c, d.End)
	}
	if _, err := ctl.Start(ctx, SimDrive, SimOpts{Short: true, Queue: true}); !errors.Is(err, ErrSimBusy) {
		t.Fatalf("a second queued run: %v", err)
	}
	if a, _ := ctl.Active(ctx); a == nil || a.ID != d.ID {
		t.Errorf("active is %+v, want the drive", a)
	}
	if total := c.End.Sub(now); total > 11*time.Minute {
		t.Errorf("the pair takes %s from the request, want 11 minutes or less", total)
	}
	// The queued charge begins where the drive left the battery, not at the schedule's value: the drive used some, so the charge starts lower.
	if d.AddedSoC >= 0 || c.FromSoC != d.AddedSoC {
		t.Errorf("drive moved the battery by %.2f, the queued charge starts %.2f away from the schedule; want the same, below zero", d.AddedSoC, c.FromSoC)
	}
	if d.FromSoC != 0 {
		t.Errorf("the first run of a chain starts at the schedule's battery, got %.2f", d.FromSoC)
	}
	pd, _ := planSimFrom(d.Kind, d.Start, d.FromSoC)
	pc, err := planSimFrom(c.Kind, c.Start, c.FromSoC)
	if err != nil {
		t.Fatal(err)
	}
	last := pd.segs[len(pd.segs)-1]
	if first := pc.segs[0]; math.Abs(first.socA-last.socB) > 0.001 {
		t.Errorf("the charge begins at %.2f%%, the drive ended at %.2f%%", first.socA, last.socB)
	}
}

// A run that does not follow another straight away starts from the schedule's battery, so a day of runs does not drift.
func TestSimRunsApartDoNotCarryTheBattery(t *testing.T) {
	var s SimState
	s.Runs = []SimRun{{ID: "a", End: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), FromSoC: 1, AddedSoC: -2}}
	if got := s.chainSoC(s.Runs[0].End); got != -1 {
		t.Errorf("a run starting as the last ends carries %.2f, want -1", got)
	}
	if got := s.chainSoC(s.Runs[0].End.Add(time.Second)); got != 0 {
		t.Errorf("a run starting later carries %.2f, want 0", got)
	}
}

// A short run is its own kind: a pod without this code knows neither kind, so it logs and skips the run rather than replanning the full run
// inside the short window. Here a "pod" that does not know the short kinds is a feed whose planner refuses them.
func TestShortKindsAreUnknownToOldCode(t *testing.T) {
	for _, k := range []SimKind{SimDriveShort, SimChargeShort} {
		if base, short := k.base(); !short || base == k || strings.HasSuffix(string(base), "_short") {
			t.Errorf("%s: base %s short %v", k, base, short)
		}
	}
	// What old code does with a kind it does not know (the default branch of its planSim) is a refusal, which withSim logs and skips.
	if _, err := planSim("drive_unknown", simStartFor(t, SimDrive)); err == nil || !strings.Contains(err.Error(), "unknown run") {
		t.Errorf("an unknown kind should be refused as unknown, got %v", err)
	}
}

// Frames inside a short run's window are the short run's, rebuilt from the stored (kind, start) alone by any pod.
func TestStoredShortRunRebuildsOnAnyPod(t *testing.T) {
	ctx := context.Background()
	start := simStartFor(t, SimDriveShort)
	p, err := planSim(SimDriveShort, start)
	if err != nil {
		t.Fatal(err)
	}
	run := SimRun{ID: "r", Kind: SimDriveShort, Start: start, End: p.end, AddedMi: round2(p.addedMi)}
	var first []State
	for pod := 0; pod < 2; pod++ {
		got, err := testSimFeed(&memSimStore{st: SimState{Runs: []SimRun{run}}}).frames(ctx, start.Add(-time.Minute), p.end.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if pod == 0 {
			first = got
			continue
		}
		if len(got) != len(first) || !reflect.DeepEqual(got[len(got)/2], first[len(first)/2]) {
			t.Fatal("two pods rebuilt different frames from the same stored run")
		}
	}
	moving := 0
	for _, st := range first {
		if !st.At.Before(start) && st.At.Before(p.end) && st.SpeedKph > 1 {
			moving++
		}
	}
	if moving == 0 {
		t.Fatal("no moving frame inside the short run's window")
	}
}
