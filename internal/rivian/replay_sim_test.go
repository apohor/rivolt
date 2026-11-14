package rivian

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memSimStore struct {
	mu sync.Mutex
	st SimState
}

func (m *memSimStore) Load(context.Context) (SimState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st, nil
}

func (m *memSimStore) Update(_ context.Context, fn func(SimState) (SimState, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := fn(m.st)
	if err != nil {
		return err
	}
	m.st = next
	return nil
}

// simStartFor finds a wall time, a few days into a loop, at which kind can be planned.
func simStartFor(t *testing.T, kind SimKind) time.Time {
	t.Helper()
	_, tl := sampleShared()
	base := tl.loopStart(100)
	for d := time.Duration(0); d < tl.span(); d += 7 * time.Minute {
		at := base.Add(d)
		if _, err := planSim(kind, at); err == nil {
			return at
		}
	}
	t.Fatalf("no start found for %s", kind)
	return time.Time{}
}

func testSimFeed(store SimStore) *ReplayFeed {
	f := NewSampleReplayFeed(nil, 24*time.Hour, nil)
	if store != nil {
		f.SetSimStore(store)
	}
	return f
}

func TestSimDriveReplacesTheScheduleAndTheOdometerRejoins(t *testing.T) {
	ctx := context.Background()
	start := simStartFor(t, SimDrive)
	p, err := planSim(SimDrive, start)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.end.Sub(start); d < 20*time.Minute || d > 30*time.Minute {
		t.Fatalf("run lasts %s, want 20-30 min", d)
	}
	store := &memSimStore{st: SimState{Runs: []SimRun{{ID: "r", Kind: SimDrive, Start: start, End: p.end, AddedMi: round2(p.addedMi)}}}}
	with, without := testSimFeed(store), testSimFeed(nil)
	from, to := start.Add(-5*time.Minute), p.end.Add(5*time.Minute)

	got, err := with.frames(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	sched, _ := without.frames(ctx, from, to)

	var drove bool
	for i, st := range got {
		if i > 0 && st.At.Before(got[i-1].At) {
			t.Fatalf("frame %d out of order", i)
		}
		if st.At.Before(start) || !st.At.Before(p.end) {
			continue
		}
		if st.Gear == "D" && st.SpeedKph > 5 {
			drove = true
		}
	}
	if !drove {
		t.Fatal("no driving frame inside the run")
	}
	// The scheduled truck sits parked through the window.
	for _, st := range sched {
		if !st.At.Before(start) && st.At.Before(p.end) && st.SpeedKph > 0 {
			t.Fatal("the schedule is not parked here; pick another start")
		}
	}
	// Odometer never runs backwards across the run, and after it the schedule's frames carry the run's miles.
	var prev float64
	for _, st := range got {
		if st.OdometerKm > 0 && st.OdometerKm+1e-6 < prev {
			t.Fatalf("odometer ran backwards at %s: %.3f < %.3f", st.At, st.OdometerKm, prev)
		}
		prev = st.OdometerKm
	}
	last := got[len(got)-1]
	want := sched[len(sched)-1].OdometerKm + p.addedMi/kmToMi
	if d := last.OdometerKm - want; d > 0.02 || d < -0.02 {
		t.Fatalf("odometer after the run %.3f km, want %.3f", last.OdometerKm, want)
	}
	// Outside the window nothing but the odometer differs.
	for i, st := range sched {
		if st.At.Before(start) {
			if got[i].At != st.At || got[i].OdometerKm != st.OdometerKm || got[i].BatteryLevelPct != st.BatteryLevelPct {
				t.Fatalf("frame before the run changed at %s", st.At)
			}
		}
	}
}

func TestSimChargePlugsChargesCompletesAndUnplugs(t *testing.T) {
	ctx := context.Background()
	start := simStartFor(t, SimCharge)
	p, err := planSim(SimCharge, start)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.end.Sub(start); d < 20*time.Minute || d > 30*time.Minute {
		t.Fatalf("run lasts %s, want 20-30 min", d)
	}
	store := &memSimStore{st: SimState{Runs: []SimRun{{ID: "r", Kind: SimCharge, Start: start, End: p.end}}}}
	got, err := testSimFeed(store).frames(ctx, start, p.end)
	if err != nil {
		t.Fatal(err)
	}
	var charging, complete, disconnected int
	var plug plugTracker
	var sawPlugAfterComplete bool
	for _, st := range got {
		plug.apply(&st)
		switch st.ChargerState {
		case "charging_active":
			charging++
			if st.ChargerPowerKW <= 0 {
				t.Fatal("charging frame at 0 kW")
			}
		case "charging_complete":
			complete++
			if st.ChargerPowerKW != 0 {
				t.Fatal("complete frame draws power")
			}
			sawPlugAfterComplete = plug.plugged
		case "charger_disconnected":
			disconnected++
		}
	}
	if charging == 0 || complete == 0 || disconnected == 0 || !sawPlugAfterComplete {
		t.Fatalf("charging=%d complete=%d disconnected=%d plugged-while-complete=%v", charging, complete, disconnected, sawPlugAfterComplete)
	}
	first, last := got[0], got[len(got)-1]
	if last.ChargerState != "charger_disconnected" || first.ChargerState != "charger_disconnected" {
		t.Fatalf("run should open and close unplugged, got %s ... %s", first.ChargerState, last.ChargerState)
	}
}

func TestSimIsRefusedNextToAScheduledDriveOrCharge(t *testing.T) {
	c, tl := sampleShared()
	// Ten minutes before a scheduled drive: no room for a run.
	var driveStart time.Time
	for _, s := range c.segs {
		if s.kind == segDrive {
			driveStart = s.start
			break
		}
	}
	at := tl.loopStart(100).Add(driveStart.Add(-10 * time.Minute).Sub(tl.start))
	for _, k := range []SimKind{SimDrive, SimCharge} {
		var ref *SimRefusal
		if _, err := planSim(k, at); !errors.As(err, &ref) {
			t.Fatalf("%s next to a scheduled drive: got %v, want a refusal", k, err)
		}
	}
	// And while it drives.
	for _, s := range c.segs {
		if s.kind == segDrive {
			mid := tl.loopStart(100).Add(s.start.Add(s.end.Sub(s.start) / 2).Sub(tl.start))
			var ref *SimRefusal
			if _, err := planSim(SimDrive, mid); !errors.As(err, &ref) {
				t.Fatalf("during a scheduled drive: got %v", err)
			}
			break
		}
	}
}

func TestSimControlOneRunAtATime(t *testing.T) {
	store := &memSimStore{}
	ctl := NewSimControl(store)
	_, tl := sampleShared()
	// Pretend now is the instant before a plannable start.
	start := simStartFor(t, SimDrive)
	ctl.now = func() time.Time { return start.Add(-simLead - time.Second) }
	run, err := ctl.Start(context.Background(), SimDrive)
	if err != nil {
		t.Fatal(err)
	}
	if run.Start.Before(start.Add(-sampleAwakeStep)) || run.Start.After(start.Add(sampleAwakeStep)) {
		t.Fatalf("run starts %s, asked about %s", run.Start, start)
	}
	if _, err := ctl.Start(context.Background(), SimCharge); !errors.Is(err, ErrSimBusy) {
		t.Fatalf("second request while one is booked: %v", err)
	}
	ctl.now = func() time.Time { return run.End.Add(time.Second) }
	if a, _ := ctl.Active(context.Background()); a != nil {
		t.Fatal("a finished run is still active")
	}
	_ = tl
}

func TestSimFrameHookSeesEverySimulatedFrameOnly(t *testing.T) {
	start := simStartFor(t, SimDrive)
	p, _ := planSim(SimDrive, start)
	store := &memSimStore{st: SimState{Runs: []SimRun{{ID: "r", Kind: SimDrive, Start: start, End: p.end, AddedMi: round2(p.addedMi)}}}}
	f := testSimFeed(store)
	n := 0
	f.SetSimFrameHook(func(run SimRun, st *State) {
		n++
		if st.At.Before(run.Start) || !st.At.Before(run.End) {
			t.Fatalf("hook saw a frame outside its run: %s", st.At)
		}
	})
	got, _ := f.frames(context.Background(), start.Add(-time.Hour), p.end.Add(time.Hour))
	if n == 0 || n >= len(got) {
		t.Fatalf("hook ran %d times over %d frames", n, len(got))
	}
}

func TestSimControlsNeedBothSwitches(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for name, tc := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"neither (prod admin)": {map[string]string{}, false},
		"asked, live allowed":  {map[string]string{"RIVOLT_SIM_CONTROLS": "true"}, false},
		"live forbidden only":  {map[string]string{"RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true"}, false},
		"asked, forbidden off": {map[string]string{"RIVOLT_SIM_CONTROLS": "true", "RIVOLT_RIVIAN_LIVE_FORBIDDEN": "false"}, false},
		"both (preview admin)": {map[string]string{"RIVOLT_SIM_CONTROLS": "true", "RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true"}, true},
	} {
		if got := SimControlsEnabled(env(tc.env)); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
