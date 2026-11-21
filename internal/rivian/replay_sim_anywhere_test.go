package rivian

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

// startsIn returns, for each routine segment satisfying want, the wall times (every five minutes) at which the truck is in it.
func startsIn(want func(sampleSeg) bool) []time.Time {
	c, tl := sampleShared()
	base := tl.loopStart(100)
	var out []time.Time
	for _, s := range c.segs {
		if !want(s) {
			continue
		}
		for at := s.start.Add(time.Minute); at.Before(s.end); at = at.Add(5 * time.Minute) {
			out = append(out, base.Add(at.Sub(tl.start)))
		}
	}
	return out
}

func awayFromHome(s sampleSeg) bool {
	return haversineMeters(s.path[0].Lat, s.path[0].Lon, SampleHome.Lat, SampleHome.Lon) > 300
}

// bookable returns the first of starts at which kind can be planned.
func bookable(t *testing.T, kind SimKind, starts []time.Time) (time.Time, simPlan) {
	t.Helper()
	for _, at := range starts {
		if p, err := planSim(kind, at); err == nil {
			return at, p
		}
	}
	t.Fatalf("%s cannot be booked at any of %d starts", kind, len(starts))
	return time.Time{}, simPlan{}
}

// A short pair runs wherever the truck is parked or plugged, and begins and ends at the truck's own place.
func checkShortRunsHere(t *testing.T, name string, starts []time.Time) {
	t.Helper()
	if len(starts) == 0 {
		t.Fatalf("%s: the routine never puts the truck there", name)
	}
	for _, kind := range []SimKind{SimDriveShort, SimChargeShort} {
		at, p := bookable(t, kind, starts)
		here := p.segs[0].path[0]
		for _, s := range p.segs {
			if s.kind == segCharge && haversineMeters(s.path[0].Lat, s.path[0].Lon, here.Lat, here.Lon) > 1 {
				t.Errorf("%s %s: the charge is not where the truck was", name, kind)
			}
		}
		if kind == SimDriveShort {
			d := segOf(p, segDrive)
			if end := d.path[len(d.path)-1]; haversineMeters(end.Lat, end.Lon, here.Lat, here.Lon) > 1 {
				t.Errorf("%s: the drive ends %.0f m from where it began", name, haversineMeters(end.Lat, end.Lon, here.Lat, here.Lon))
			}
			if mi := d.odoB - d.odoA; mi < 3 || mi > 4.2 {
				t.Errorf("%s: short drive is %.2f mi, want about 3.6", name, mi)
			}
			if p.origin != here {
				t.Errorf("%s: the nav origin %v is not the truck's place %v", name, p.origin, here)
			}
		}
		if p.end.Sub(at) > 8*time.Minute {
			t.Errorf("%s %s: takes %s", name, kind, p.end.Sub(at))
		}
	}
}

func TestShortRunsBookAwayFromHome(t *testing.T) {
	checkShortRunsHere(t, "away", startsIn(func(s sampleSeg) bool { return s.kind == segParked && s.power != "sleep" && awayFromHome(s) }))
}

func TestShortRunsBookWhileAsleep(t *testing.T) {
	checkShortRunsHere(t, "asleep", startsIn(func(s sampleSeg) bool { return s.kind == segParked && s.power == "sleep" }))
	// Asleep away from home as well.
	checkShortRunsHere(t, "asleep away", startsIn(func(s sampleSeg) bool { return s.kind == segParked && s.power == "sleep" && awayFromHome(s) }))
}

func TestShortRunsBookMidScheduledCharge(t *testing.T) {
	starts := startsIn(func(s sampleSeg) bool { return s.kind == segCharge })
	checkShortRunsHere(t, "mid charge", starts)
	// The routine's own charge is not a "next scheduled charge" the run must stay clear of: a start in its first minutes books.
	if _, err := planSim(SimChargeShort, starts[0]); err != nil {
		t.Errorf("first five minutes of a scheduled charge: %v", err)
	}
}

// A moving truck cannot be handed to a run without a jump in location, so a scheduled drive refuses with the time it ends.
func TestShortRunRefusedMidScheduledDriveWithItsEnd(t *testing.T) {
	c, tl := sampleShared()
	base := tl.loopStart(100)
	var n int
	for _, s := range c.segs {
		if s.kind != segDrive {
			continue
		}
		at := base.Add(s.start.Add(s.end.Sub(s.start) / 2).Sub(tl.start))
		endsAt := base.Add(s.end.Sub(tl.start)).UTC().Format("15:04Z")
		for _, kind := range []SimKind{SimDriveShort, SimChargeShort, SimDrive} {
			_, err := planSim(kind, at)
			var ref *SimRefusal
			if err == nil {
				t.Fatalf("%s booked in the middle of a scheduled drive", kind)
			}
			if kind != SimDrive {
				if ref = asRefusal(err); ref == nil || !strings.Contains(ref.Error(), endsAt) {
					t.Errorf("%s: refusal %q does not say the drive ends %s", kind, err, endsAt)
				}
			}
		}
		n++
	}
	if n == 0 {
		t.Fatal("no scheduled drive in the routine")
	}
}

func asRefusal(err error) *SimRefusal {
	r, _ := err.(*SimRefusal)
	return r
}

// The routine resumes where the run left it: the truck's place and odometer carry on, and the battery has no step at the run's end.
func TestRoutineResumesWithoutJumpsAfterARunAnywhere(t *testing.T) {
	ctx := context.Background()
	cases := map[string][]time.Time{
		"away":   startsIn(func(s sampleSeg) bool { return s.kind == segParked && s.power != "sleep" && awayFromHome(s) }),
		"asleep": startsIn(func(s sampleSeg) bool { return s.kind == segParked && s.power == "sleep" }),
		"charge": startsIn(func(s sampleSeg) bool { return s.kind == segCharge }),
	}
	for name, starts := range cases {
		for _, kind := range []SimKind{SimDriveShort, SimChargeShort} {
			start, p := bookable(t, kind, starts)
			run := SimRun{ID: "r", Kind: kind, Start: start, End: p.end, AddedMi: round2(p.addedMi), AddedSoC: p.addSoC, EndSoC: p.endSoC}
			with, without := testSimFeed(&memSimStore{st: SimState{Runs: []SimRun{run}}}), testSimFeed(nil)
			from, to := start.Add(-2*time.Minute), p.end.Add(simEase+10*time.Minute)
			got, err := with.frames(ctx, from, to)
			if err != nil {
				t.Fatal(err)
			}
			sched, _ := without.frames(ctx, from, to)
			var before, after int = -1, -1
			for i, st := range got {
				if st.At.Before(start) {
					before = i
				}
				if after < 0 && !st.At.Before(p.end) {
					after = i
				}
			}
			if before < 0 || after < 0 {
				t.Fatalf("%s %s: no frames around the run", name, kind)
			}
			b, a := got[before], got[after]
			if d := haversineMeters(b.Latitude, b.Longitude, a.Latitude, a.Longitude); d > 5 {
				t.Errorf("%s %s: the truck jumps %.0f m across the run", name, kind, d)
			}
			if b.OdometerKm > 0 {
				// The odometer rises by the run's miles and no more.
				if rise := a.OdometerKm - b.OdometerKm; math.Abs(rise-p.addedMi/kmToMi) > 0.05 {
					t.Errorf("%s %s: odometer rises %.3f km across the run, want %.3f", name, kind, rise, p.addedMi/kmToMi)
				}
			}
			// The run's own last frame and the first frame after it differ by one frame's worth of battery at most (no step back to the schedule).
			if prev := got[after-1]; math.Abs(a.BatteryLevelPct-prev.BatteryLevelPct) > 0.5 {
				t.Errorf("%s %s: battery steps %.2f -> %.2f at the run's end", name, kind, prev.BatteryLevelPct, a.BatteryLevelPct)
			}
			// After the ease the battery is the schedule's again.
			last, sl := got[len(got)-1], sched[len(sched)-1]
			if last.At.Equal(sl.At) && math.Abs(last.BatteryLevelPct-sl.BatteryLevelPct) > 0.01 {
				t.Errorf("%s %s: battery %.2f after the ease, schedule %.2f", name, kind, last.BatteryLevelPct, sl.BatteryLevelPct)
			}
		}
	}
}

// Prod keeps no sim route: planning stays fixture-only and a booked run needs both switches (see TestSimControlsNeedBothSwitches).
func TestAnywherePlanNeverNamesSampleHomeAway(t *testing.T) {
	_, p := bookable(t, SimDriveShort, startsIn(func(s sampleSeg) bool { return s.kind == segParked && awayFromHome(s) }))
	if p.originName != "Sample start" || p.errandName != "Sample stop" {
		t.Errorf("away nav names %q -> %q", p.originName, p.errandName)
	}
}

// The Live card waits for a Rivian session to report; the short fast charge carries one wherever it happens.
func TestShortChargeReportsALiveSessionAnywhere(t *testing.T) {
	for name, starts := range map[string][]time.Time{
		"home": {simStartFor(t, SimChargeShort)},
		"away": startsIn(func(s sampleSeg) bool { return s.kind == segParked && awayFromHome(s) }),
	} {
		_, p := bookable(t, SimChargeShort, starts)
		ch := segOf(p, segCharge)
		if ch.billedRate <= 0 || ch.billedBy == "" {
			t.Fatalf("%s: the fast charge has no billed rate, so no live session", name)
		}
		if fr := ch.frameAt(ch.frames() - 1); fr.SessionPrice <= 0 {
			t.Errorf("%s: last frame has no session price", name)
		}
	}
}

// A scheduled charge that would start inside the window does not refuse a short run: the run pre-empts it.
func TestScheduledChargeInsideTheWindowDoesNotRefuse(t *testing.T) {
	c, tl := sampleShared()
	base := tl.loopStart(100)
	var tried int
	for i, s := range c.segs {
		if s.kind != segCharge || i == 0 || c.segs[i-1].kind != segParked {
			continue
		}
		// A minute before the charge starts: the run (6 to 11 minutes) reaches into it.
		at := base.Add(s.start.Add(-time.Minute).Sub(tl.start))
		if c.segs[i-1].start.After(s.start.Add(-90 * time.Second)) {
			continue
		}
		// (The drive may refuse for a low battery, which is why the truck charges there; the charge is the case.)
		if _, err := planSim(SimChargeShort, at); err != nil {
			t.Errorf("a minute before a scheduled charge: %v", err)
		}
		tried++
	}
	if tried == 0 {
		t.Fatal("no scheduled charge with a parked stretch before it")
	}
}

// A run booked while an earlier one is still easing starts from the eased battery, and the ease it leaves is measured from that.
func TestRunBookedDuringAnEaseStartsFromTheEasedBattery(t *testing.T) {
	ctx := context.Background()
	start := simStartFor(t, SimDriveShort)
	p, err := planSim(SimDriveShort, start)
	if err != nil {
		t.Fatal(err)
	}
	first := SimRun{ID: "a", Kind: SimDriveShort, Start: start, End: p.end, AddedMi: round2(p.addedMi), AddedSoC: p.addSoC, EndSoC: p.endSoC}
	ctl := NewSimControl(&memSimStore{st: SimState{Runs: []SimRun{first}}})
	now := p.end.Add(simEase / 2)
	ctl.now = func() time.Time { return now }
	run, err := ctl.Start(ctx, SimChargeShort, SimOpts{Short: true})
	if err != nil {
		t.Skipf("the routine is not free half an ease after this run: %v", err)
	}
	if want := first.AddedSoC / 2; math.Abs(run.FromSoC-want) > 0.3 {
		t.Errorf("second run starts %.2f points from the schedule, want about %.2f (half of the first run's %.2f)", run.FromSoC, want, first.AddedSoC)
	}
	if first.AddedSoC >= 0 {
		t.Errorf("the drive should have used battery, got %.2f", first.AddedSoC)
	}
}

// While easing the battery stays at the car's charge limit, and a run that itself ended above the limit gives the overshoot back smoothly.
func TestEaseHoldsTheChargeLimit(t *testing.T) {
	end := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s := SimState{Runs: []SimRun{{ID: "a", End: end, AddedSoC: 3}}}
	if got := s.easedBattery(end.Add(time.Minute), 80, 80); got != 80 {
		t.Errorf("a truck at its limit rose to %.2f", got)
	}
	if got := s.easedBattery(end.Add(time.Minute), 70, 80); math.Abs(got-72.975) > 0.01 {
		t.Errorf("below the limit the offset applies: %.3f", got)
	}
	// Ended 3 points over the limit: the first frame after is the run's end, and the overshoot leaves linearly.
	s.Runs[0].EndSoC = 83
	if got := s.easedBattery(end, 80, 80); math.Abs(got-83) > 1e-9 {
		t.Errorf("at the run's end %.3f, want 83", got)
	}
	if got := s.easedBattery(end.Add(simEase/2), 80, 80); math.Abs(got-81.5) > 1e-9 {
		t.Errorf("halfway %.3f, want 81.5", got)
	}
	if got := s.easedBattery(end.Add(simEase), 80, 80); got != 80 {
		t.Errorf("after the ease %.3f, want 80", got)
	}
}

// The refusal for a scheduled drive carries when it is free, as a field the booking script can read.
func TestRefusalCarriesFreeAt(t *testing.T) {
	c, tl := sampleShared()
	base := tl.loopStart(100)
	for _, s := range c.segs {
		if s.kind != segDrive {
			continue
		}
		at := base.Add(s.start.Add(s.end.Sub(s.start) / 2).Sub(tl.start))
		_, err := planSim(SimDriveShort, at)
		r := asRefusal(err)
		if r == nil || !r.FreeAt.Equal(base.Add(s.end.Sub(tl.start))) {
			t.Fatalf("refusal %v does not carry the drive's end %s", err, base.Add(s.end.Sub(tl.start)))
		}
		return
	}
	t.Fatal("no scheduled drive")
}
