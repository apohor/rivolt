package rivian

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// simStartForShort finds a wall time at which a short run of kind can be planned.
func simStartForShort(t *testing.T, kind SimKind) time.Time {
	t.Helper()
	_, tl := sampleShared()
	base := tl.loopStart(100)
	for d := time.Duration(0); d < tl.span(); d += 7 * time.Minute {
		at := base.Add(d)
		if _, err := planSim(kind, at, true); err == nil {
			return at
		}
	}
	t.Fatalf("no start found for a short %s", kind)
	return time.Time{}
}

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
	start := simStartForShort(t, SimDrive)
	short, err := planSim(SimDrive, start, true)
	if err != nil {
		t.Fatal(err)
	}
	full, err := planSim(SimDrive, simStartFor(t, SimDrive), false)
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
	if d := short.end.Sub(start); d < 6*time.Minute || d > 10*time.Minute {
		t.Errorf("short drive run lasts %s, want 6-10 min", d)
	}
	if ds.odoB-ds.odoA < 3 {
		t.Errorf("short drive is %.1f mi, too short to show start, middle and end", ds.odoB-ds.odoA)
	}
	if first, last := short.segs[0], short.segs[len(short.segs)-1]; first.kind != segParked || last.kind != segParked || segOf(short, segDrive).kind != segDrive {
		t.Errorf("shape: want parked, drive, parked")
	}
}

// The short charge is the same power for fewer minutes: energy added is kW times time.
func TestShortChargeIsTheSamePowerForFewerMinutes(t *testing.T) {
	start := simStartForShort(t, SimCharge)
	p, err := planSim(SimCharge, start, true)
	if err != nil {
		t.Fatal(err)
	}
	c := segOf(p, segCharge)
	if c.peakKW != sampleL2KW {
		t.Fatalf("power %.1f kW, want %.1f", c.peakKW, sampleL2KW)
	}
	hours := c.end.Sub(c.start).Hours()
	if math.Abs(hours*60-simShortChargeMin) > 0.1 {
		t.Errorf("charging slice %.2f min, want %d", hours*60, simShortChargeMin)
	}
	if kwh := (c.socB - c.socA) / 100 * samplePackKWh; math.Abs(kwh-c.peakKW*hours) > 0.01 {
		t.Errorf("energy %.3f kWh, power x time is %.3f", kwh, c.peakKW*hours)
	}
	if d := p.end.Sub(start); d < 6*time.Minute || d > 9*time.Minute {
		t.Errorf("short charge run lasts %s, want 6-9 min", d)
	}
}

// A run is rebuilt from (kind, start, short) by whichever pod reads the stored document, so the stored flag must select the short frames.
func TestStoredShortFlagSelectsTheShortFrames(t *testing.T) {
	ctx := context.Background()
	start := simStartForShort(t, SimDrive)
	p, err := planSim(SimDrive, start, true)
	if err != nil {
		t.Fatal(err)
	}
	run := SimRun{ID: "r", Kind: SimDrive, Start: start, End: p.end, AddedMi: round2(p.addedMi), Short: true}
	got, err := testSimFeed(&memSimStore{st: SimState{Runs: []SimRun{run}}}).frames(ctx, start.Add(-time.Minute), p.end.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	moving := 0
	for _, st := range got {
		if !st.At.Before(start) && st.At.Before(p.end) && st.SpeedKph > 1 {
			moving++
		}
	}
	if moving == 0 {
		t.Fatal("no moving frame inside the short run's window")
	}
	// Without the flag the same window would hold the full-length run: a different end, which is why the flag is stored.
	full, _ := planSim(SimDrive, start, false)
	if full.end.Equal(p.end) {
		t.Error("short and full runs end together: the flag would not matter")
	}
}

func TestSimQueueBooksTheNextRunAfterTheOneUnderWay(t *testing.T) {
	ctx := context.Background()
	// A start where a short drive can be planned and a short charge right after it.
	_, tl := sampleShared()
	var start time.Time
	for d := time.Duration(0); d < tl.span(); d += 7 * time.Minute {
		at := tl.loopStart(100).Add(d)
		p, err := planSim(SimDrive, at, true)
		if err != nil {
			continue
		}
		if _, err := planSim(SimCharge, p.end, true); err == nil {
			start = at
			break
		}
	}
	if start.IsZero() {
		t.Skip("the sample day has no parked stretch long enough for a drive then a charge")
	}
	ctl := NewSimControl(&memSimStore{})
	ctl.now = func() time.Time { return start.Add(-simLead - time.Second) }
	d, err := ctl.Start(ctx, SimDrive, SimOpts{Short: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Start(ctx, SimCharge, SimOpts{Short: true}); !errors.Is(err, ErrSimBusy) {
		t.Fatalf("without queue while one runs: %v", err)
	}
	c, err := ctl.Start(ctx, SimCharge, SimOpts{Short: true, Queue: true})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Start.Equal(d.End) || !c.Short || c.Kind != SimCharge {
		t.Errorf("queued run %+v does not begin at the drive's end %s", c, d.End)
	}
	if _, err := ctl.Start(ctx, SimDrive, SimOpts{Short: true, Queue: true}); !errors.Is(err, ErrSimBusy) {
		t.Fatalf("a second queued run: %v", err)
	}
	if a, _ := ctl.Active(ctx); a == nil || a.ID != d.ID {
		t.Errorf("active is %+v, want the drive", a)
	}
}
