package tirelow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

type fakeSource struct {
	vs      []Vehicle
	st      *rivian.State
	at      time.Time
	claims  [][]string
	claimOK bool
}

func (f *fakeSource) Vehicles(context.Context) ([]Vehicle, error) { return f.vs, nil }
func (f *fakeSource) Latest(uuid.UUID, string) (*rivian.State, time.Time) {
	return f.st, f.at
}
func (f *fakeSource) Claim(_ context.Context, _, _ uuid.UUID, _ time.Time, wheels []string) (bool, error) {
	f.claims = append(f.claims, wheels)
	return f.claimOK, nil
}
func (f *fakeSource) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

type rig struct {
	t    *testing.T
	src  *fakeSource
	job  *Job
	now  time.Time
	sent [][]string
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	r.src = &fakeSource{vs: []Vehicle{{UserID: uuid.New(), VehicleID: uuid.New(), RivianID: "r", Model: "R1S"}}, claimOK: true}
	r.job = &Job{
		Source: r.src,
		Now:    func() time.Time { return r.now },
		Deliver: func(_ context.Context, _ uuid.UUID, _ string, wheels []string) error {
			r.sent = append(r.sent, wheels)
			return nil
		},
	}
	return r
}

// tick moves the clock by d and ticks with this state, fresh as of now unless stale is set.
func (r *rig) tick(d time.Duration, st rivian.State, stale bool) {
	r.now = r.now.Add(d)
	r.src.st = &st
	r.src.at = r.now
	if stale {
		r.src.at = r.now.Add(-time.Hour)
	}
	r.job.Tick(context.Background())
}

func state(gear string, fl, fr, rl, rr string) rivian.State {
	return rivian.State{Gear: gear, TirePressureStatusFL: fl, TirePressureStatusFR: fr, TirePressureStatusRL: rl, TirePressureStatusRR: rr}
}

func TestAlertsOnlyAfterTheTruckHasSaidLowForThirtyMinutesInPark(t *testing.T) {
	r := newRig(t)
	low := state("P", "Low", "Normal", "Normal", "Normal")
	r.tick(0, low, false)
	r.tick(29*time.Minute, low, false)
	if len(r.sent) != 0 {
		t.Fatalf("alerted after 29 minutes: %v", r.sent)
	}
	r.tick(time.Minute, low, false)
	if len(r.sent) != 1 || len(r.sent[0]) != 1 || r.sent[0][0] != "fl" {
		t.Fatalf("sent = %v, want the front left once", r.sent)
	}
	// Once: later ticks add nothing.
	r.tick(time.Minute, low, false)
	r.tick(time.Hour, low, false)
	if len(r.sent) != 1 || len(r.src.claims) != 1 {
		t.Errorf("repeated: sent %v, claims %v", r.sent, r.src.claims)
	}
}

func TestNeverWhileNotInParkAndNeverOnOurOwnFloor(t *testing.T) {
	r := newRig(t)
	r.tick(0, state("D", "Low", "Normal", "Normal", "Normal"), false)
	r.tick(time.Hour, state("D", "Low", "Normal", "Normal", "Normal"), false)
	if len(r.sent) != 0 {
		t.Fatalf("alerted in D: %v", r.sent)
	}
	// Parked now, the clock has run in the meantime: it alerts at once.
	r.tick(time.Minute, state("P", "Low", "Normal", "Normal", "Normal"), false)
	if len(r.sent) != 1 {
		t.Fatalf("a long-low wheel parked at last did not alert: %v", r.sent)
	}
	// A low pressure number with a normal status is not low: only the truck's word counts.
	r2 := newRig(t)
	s := state("P", "Normal", "Normal", "Normal", "Normal")
	s.TirePressureFLBar, s.TirePressureFRBar = 1.2, 1.2
	r2.tick(0, s, false)
	r2.tick(2*time.Hour, s, false)
	if len(r2.sent) != 0 {
		t.Errorf("alerted on a pressure the truck calls normal: %v", r2.sent)
	}
}

func TestUnknownAndMissingWordsNeverAlertAndNeverClear(t *testing.T) {
	r := newRig(t)
	r.tick(0, state("P", "Mystery", "", "", ""), false)
	r.tick(time.Hour, state("P", "Mystery", "", "", ""), false)
	if len(r.sent) != 0 {
		t.Fatalf("alerted on a word with no claim: %v", r.sent)
	}
	// A wheel low, then a frame that says nothing about it: the clock keeps running.
	r = newRig(t)
	r.tick(0, state("P", "Low", "", "", ""), false)
	r.tick(20*time.Minute, state("P", "Mystery", "", "", ""), false)
	r.tick(11*time.Minute, state("P", "Low", "", "", ""), false)
	if len(r.sent) != 1 {
		t.Errorf("an unfamiliar word reset the clock: %v", r.sent)
	}
}

func TestAStaleStateSaysNothingAndTheClockSurvivesAGap(t *testing.T) {
	r := newRig(t)
	low := state("P", "Low", "Normal", "Normal", "Normal")
	r.tick(0, low, false)
	// The truck sleeps for hours: stale states count as neither low nor fine, and trigger nothing.
	r.tick(2*time.Hour, low, true)
	if len(r.sent) != 0 {
		t.Fatalf("a stale state alerted: %v", r.sent)
	}
	// It wakes, still low on a fresh frame, parked: low since long ago, no fresh "not low" in between: alert.
	r.tick(time.Minute, low, false)
	if len(r.sent) != 1 {
		t.Errorf("the common case (low overnight, woken) did not alert: %v", r.sent)
	}
}

func TestAFreshNormalResetsTheThirtyMinutes(t *testing.T) {
	r := newRig(t)
	low, ok := state("P", "Low", "Normal", "Normal", "Normal"), state("P", "Normal", "Normal", "Normal", "Normal")
	r.tick(0, low, false)
	r.tick(25*time.Minute, ok, false)
	r.tick(time.Minute, low, false) // low again: the clock starts over
	r.tick(20*time.Minute, low, false)
	if len(r.sent) != 0 {
		t.Fatalf("alerted 20 minutes after the wheel was last fine: %v", r.sent)
	}
	r.tick(10*time.Minute, low, false)
	if len(r.sent) != 1 {
		t.Errorf("sent = %v, want one alert after a full 30 minutes", r.sent)
	}
}

func TestAFlickerDoesNotMakeANewAlertButTwelveHoursOfNormalEndsTheEpisode(t *testing.T) {
	r := newRig(t)
	low, ok := state("P", "Low", "Normal", "Normal", "Normal"), state("P", "Normal", "Normal", "Normal", "Normal")
	r.tick(0, low, false)
	r.tick(31*time.Minute, low, false)
	if len(r.sent) != 1 {
		t.Fatalf("setup: %v", r.sent)
	}
	// Normal for a while, then low again for 30 minutes: the same episode, no second alert.
	for i := 0; i < 10; i++ {
		r.tick(time.Minute, ok, false)
	}
	r.tick(time.Minute, low, false)
	r.tick(31*time.Minute, low, false)
	if len(r.sent) != 1 {
		t.Fatalf("a flicker made a second alert: %v", r.sent)
	}
	// 12 hours of back-to-back fresh Normal ends the episode; the wheel can then alert again (the database cooldown is
	// what keeps it quiet within a week, and the fake source grants the claim).
	for i := 0; i < 12*60+1; i++ {
		r.tick(time.Minute, ok, false)
	}
	r.tick(time.Minute, low, false)
	r.tick(31*time.Minute, low, false)
	if len(r.sent) != 2 {
		t.Errorf("sent = %v, want a second alert in a new episode", r.sent)
	}
}

func TestAGapWithNoFramesNeverCountsAsNormal(t *testing.T) {
	r := newRig(t)
	low, ok := state("P", "Low", "Normal", "Normal", "Normal"), state("P", "Normal", "Normal", "Normal", "Normal")
	r.tick(0, low, false)
	r.tick(31*time.Minute, low, false)
	// One fresh Normal, then 13 hours with no fresh frame, then Normal again: only the two instants count, not the gap.
	r.tick(time.Minute, ok, false)
	r.tick(13*time.Hour, ok, true)
	r.tick(time.Minute, ok, false)
	r.tick(time.Minute, low, false)
	r.tick(31*time.Minute, low, false)
	if len(r.sent) != 1 {
		t.Errorf("a gap ended the episode: %v", r.sent)
	}
}

func TestASecondWheelAlertsAgainAndAlreadyCoveredWheelsDoNot(t *testing.T) {
	r := newRig(t)
	r.tick(0, state("P", "Low", "Normal", "Normal", "Normal"), false)
	r.tick(31*time.Minute, state("P", "Low", "Normal", "Normal", "Normal"), false)
	// Another wheel goes low inside the cooldown: after its own 30 minutes, an alert naming both.
	both := state("P", "Low", "Normal", "Normal", "Low")
	r.tick(time.Minute, both, false)
	r.tick(31*time.Minute, both, false)
	if len(r.sent) != 2 || len(r.sent[1]) != 2 || r.sent[1][0] != "fl" || r.sent[1][1] != "rr" {
		t.Fatalf("sent = %v, want the front left, then the front left and rear right", r.sent)
	}
	// When the database says a recent alert already covers it (another pod, a restart), nothing is sent and it is not
	// claimed again every tick.
	r2 := newRig(t)
	r2.src.claimOK = false
	low := state("P", "Low", "Normal", "Normal", "Normal")
	r2.tick(0, low, false)
	r2.tick(31*time.Minute, low, false)
	r2.tick(time.Minute, low, false)
	r2.tick(time.Minute, low, false)
	if len(r2.sent) != 0 || len(r2.src.claims) != 1 {
		t.Errorf("sent %v, claims %v: want no send and one claim", r2.sent, r2.src.claims)
	}
}

func TestThisPodNotRunningTheVehicleDoesNothing(t *testing.T) {
	r := newRig(t)
	r.job.Tick(context.Background()) // Latest is nil
	r.tick(time.Hour, state("P", "Low", "", "", ""), false)
	r.src.st = nil
	r.job.Tick(context.Background())
	if len(r.sent) != 0 {
		t.Errorf("sent = %v", r.sent)
	}
}
