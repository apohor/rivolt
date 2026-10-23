package leftopen

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

var t0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// parked is a truck parked, locked and asleep with everything closed.
func parked() *rivian.State {
	return &rivian.State{
		BatteryLevelPct: 60, OdometerKm: 30000, Gear: "P", PowerState: "sleep", Locked: true,
		DoorsClosed: true, FrunkClosed: true, LiftgateClosed: true, TailgateClosed: true, TonneauClosed: true,
	}
}

func TestOpenClosuresIsOnlyWhatIsPositivelyOpen(t *testing.T) {
	st := parked()
	if got := OpenClosures(st); len(got) != 0 {
		t.Errorf("all closed: %v", got)
	}
	st.FrunkClosed, st.DoorsClosed = false, false
	if got := strings.Join(OpenClosures(st), ","); got != "frunk,door" {
		t.Errorf("frunk and a door: %s", got)
	}
	// Windows are not part of v1: an open window changes nothing.
	st = parked()
	st.WindowFrontLeftClosed = "open"
	if got := OpenClosures(st); len(got) != 0 {
		t.Errorf("a window counted: %v", got)
	}
	// A state with no real data (nothing read yet) reports nothing, though every closure bool is false.
	if got := OpenClosures(&rivian.State{}); got != nil {
		t.Errorf("empty state: %v", got)
	}
	if OpenClosures(nil) != nil {
		t.Error("nil state")
	}
}

func TestWalkedAway(t *testing.T) {
	at := t0
	for _, c := range []struct {
		name string
		edit func(*rivian.State)
		age  time.Duration
		want bool
	}{
		{"asleep in P", func(s *rivian.State) {}, time.Hour, true},
		{"standby in P", func(s *rivian.State) { s.PowerState = "standby" }, time.Hour, true},
		{"asleep, a state 11 h old still says so", func(s *rivian.State) {}, 11 * time.Hour, true},
		{"asleep, a state over 12 h old says nothing", func(s *rivian.State) {}, 13 * time.Hour, false},
		{"awake and locked, fresh", func(s *rivian.State) { s.PowerState = "ready" }, 5 * time.Minute, true},
		{"awake and locked, 11 minutes old", func(s *rivian.State) { s.PowerState = "ready" }, 11 * time.Minute, false},
		{"awake and not locked", func(s *rivian.State) { s.PowerState = "ready"; s.Locked = false }, time.Minute, false},
		{"power state unknown", func(s *rivian.State) { s.PowerState = "" }, time.Minute, false},
		{"not in P", func(s *rivian.State) { s.Gear = "D" }, time.Minute, false},
		{"gear unknown", func(s *rivian.State) { s.Gear = "" }, time.Minute, false},
		{"charging", func(s *rivian.State) { s.ChargerState = "charging_active" }, time.Minute, false},
		{"charge complete, asleep", func(s *rivian.State) { s.ChargerState = "charging_complete" }, time.Minute, true},
	} {
		st := parked()
		c.edit(st)
		if got := WalkedAway(st, at, at.Add(c.age)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if WalkedAway(nil, at, at) || WalkedAway(parked(), time.Time{}, at) {
		t.Error("no state or no time must not read as walked away")
	}
}

type fakeSource struct {
	vehicles []Vehicle
	state    *rivian.State
	at       time.Time
	claimed  map[string]bool
	claims   int
	pruned   int
}

func (f *fakeSource) Vehicles(context.Context) ([]Vehicle, error)         { return f.vehicles, nil }
func (f *fakeSource) Latest(uuid.UUID, string) (*rivian.State, time.Time) { return f.state, f.at }
func (f *fakeSource) Claim(_ context.Context, _, vid uuid.UUID, start time.Time) (bool, error) {
	f.claims++
	k := vid.String() + start.String()
	if f.claimed[k] {
		return false, nil
	}
	f.claimed[k] = true
	return true, nil
}
func (f *fakeSource) Prune(context.Context, time.Time) (int64, error) { f.pruned++; return 0, nil }

type sent struct {
	model string
	kinds []string
	mins  int
}

func rig() (*Job, *fakeSource, *[]sent, *time.Time) {
	now := t0
	src := &fakeSource{vehicles: []Vehicle{{UserID: uuid.New(), VehicleID: uuid.New(), RivianID: "veh-1", Model: "R1S"}}, claimed: map[string]bool{}}
	var out []sent
	j := &Job{Source: src, Now: func() time.Time { return now },
		Deliver: func(_ context.Context, _ uuid.UUID, model string, kinds []string, mins int) error {
			out = append(out, sent{model, kinds, mins})
			return nil
		}}
	return j, src, &out, &now
}

func TestAnAlertFiresOnceWhenAClosureStaysOpenAfterTheTruckSleeps(t *testing.T) {
	j, src, out, now := rig()
	// The frunk is opened and the owner walks away; the truck sleeps. Frames stop: the state's own time stays put.
	open := parked()
	open.PowerState, open.FrunkClosed = "ready", false
	src.state, src.at = open, t0
	j.Tick(context.Background()) // first seen open, on a fresh state
	if len(*out) != 0 {
		t.Fatal("alerted at once")
	}
	slept := parked()
	slept.FrunkClosed = false
	src.state = slept // asleep; no new frame, so src.at is unchanged
	for _, d := range []time.Duration{5, 10, 14} {
		*now = t0.Add(d * time.Minute)
		j.Tick(context.Background())
	}
	if len(*out) != 0 {
		t.Fatalf("alerted before %v: %v", OpenFor, *out)
	}
	*now = t0.Add(OpenFor)
	j.Tick(context.Background())
	if len(*out) != 1 || (*out)[0].model != "R1S" || strings.Join((*out)[0].kinds, ",") != "frunk" || (*out)[0].mins != 15 {
		t.Fatalf("alert = %+v", *out)
	}
	// And not again, tick after tick.
	for i := 1; i <= 30; i++ {
		*now = t0.Add(OpenFor + time.Duration(i)*time.Minute)
		j.Tick(context.Background())
	}
	if len(*out) != 1 || src.claims != 1 {
		t.Errorf("repeated: %d alerts, %d claims", len(*out), src.claims)
	}
}

func TestClosingEndsTheEpisodeAndANewOneCanAlertAgain(t *testing.T) {
	j, src, out, now := rig()
	st := parked()
	st.DoorsClosed = false
	src.state, src.at = st, t0
	j.Tick(context.Background())
	*now = t0.Add(10 * time.Minute)
	src.state, src.at = parked(), *now // closed
	j.Tick(context.Background())
	*now = t0.Add(30 * time.Minute) // would have been 30 minutes open
	st2 := parked()
	st2.DoorsClosed = false
	src.state, src.at = st2, *now
	j.Tick(context.Background()) // a new episode starts now
	if len(*out) != 0 {
		t.Fatalf("a closed door still counted: %v", *out)
	}
	*now = t0.Add(30*time.Minute + OpenFor)
	j.Tick(context.Background())
	if len(*out) != 1 {
		t.Errorf("the new episode did not alert: %v", *out)
	}
}

func TestUnknownInputsMeanNoAlertAndAnR2ShapedTruckNeedsParallaxToSupplyThem(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*rivian.State)
		want int
	}{
		// An R2's vehicleState sends no gear and no power state; the Parallax frames fill them. Without them: nothing.
		{"R2 before Parallax has supplied gear and power", func(s *rivian.State) { s.Gear, s.PowerState = "", "" }, 0},
		{"R2 with Parallax P and sleep", func(s *rivian.State) {}, 1},
		{"gear unknown", func(s *rivian.State) { s.Gear = "" }, 0},
		{"power unknown", func(s *rivian.State) { s.PowerState = "" }, 0},
		{"awake and unlocked", func(s *rivian.State) { s.PowerState = "ready"; s.Locked = false }, 0},
		{"in drive", func(s *rivian.State) { s.Gear = "D" }, 0},
	} {
		j, src, out, now := rig()
		st := parked()
		st.TonneauClosed = false
		c.edit(st)
		src.state, src.at = st, t0
		// Fresh enough to start the episode: an awake locked truck is vouched for only on a fresh state, so keep the
		// frame fresh throughout for those cases.
		j.Tick(context.Background())
		*now = t0.Add(OpenFor)
		if st.PowerState == "ready" {
			src.at = *now
		}
		j.Tick(context.Background())
		if len(*out) != c.want {
			t.Errorf("%s: %d alerts, want %d", c.name, len(*out), c.want)
		}
	}
}

func TestNothingHappensWithoutAStateOrOnAnOldOne(t *testing.T) {
	j, src, out, now := rig()
	j.Tick(context.Background()) // this pod does not run the vehicle: no state
	st := parked()
	st.FrunkClosed = false
	src.state, src.at = st, t0.Add(-time.Hour) // an old state never starts an episode
	j.Tick(context.Background())
	*now = t0.Add(2 * OpenFor)
	j.Tick(context.Background())
	if len(*out) != 0 || src.claims != 0 {
		t.Errorf("alerted on nothing: %v, %d claims", *out, src.claims)
	}
}

func TestAnotherPodsClaimOrAFailedDeliveryStopsASecondSend(t *testing.T) {
	j, src, out, now := rig()
	st := parked()
	st.FrunkClosed = false
	src.state, src.at = st, t0
	j.Tick(context.Background())
	// Another pod has already alerted this episode.
	src.claimed[src.vehicles[0].VehicleID.String()+t0.String()] = true
	*now = t0.Add(OpenFor)
	j.Tick(context.Background())
	if len(*out) != 0 {
		t.Errorf("sent despite another pod's claim: %v", *out)
	}
	// A delivery that fails (no device wants it) is not retried every minute.
	j2, src2, _, now2 := rig()
	src2.state, src2.at = st, t0
	fails := 0
	j2.Deliver = func(context.Context, uuid.UUID, string, []string, int) error { fails++; return errors.New("no device") }
	j2.Tick(context.Background())
	*now2 = t0.Add(OpenFor)
	for i := 0; i < 5; i++ {
		j2.Tick(context.Background())
	}
	if fails != 1 {
		t.Errorf("delivery tried %d times", fails)
	}
}
