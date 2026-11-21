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
			run := SimRun{ID: "r", Kind: kind, Start: start, End: p.end, AddedMi: round2(p.addedMi), AddedSoC: p.addSoC}
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
