package rivian

import (
	"context"
	"testing"
	"time"
)

// gearEnum returns the Parallax dynamics.vehicle.gear value that maps
// to the gear letter g (the inverse of gearFromParallax).
func gearEnum(t *testing.T, g string) uint64 {
	t.Helper()
	for v := uint64(0); v < 16; v++ {
		if gearFromParallax(v) == g {
			return v
		}
	}
	t.Fatalf("no Parallax enum maps to gear %q", g)
	return 0
}

func newGearMonitor(t *testing.T) *StateMonitor {
	t.Helper()
	m := NewStateMonitor(nil, nil)
	m.parallaxDriveDynamics = true
	m.cache["vid-1"] = &State{
		VehicleID: "vid-1", At: time.Now(), Gear: "P", BatteryLevelPct: 40, OdometerKm: 135000,
		ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: 200,
	}
	return m
}

// 2026-10-10: after every Parallax resubscribe the cloud replays the
// last gear snapshot (D, from repositioning before plugging in) with
// its ORIGINAL ts_ms, into a car that is fast-charging. It must not be
// applied: no cached gear change, no "fresh Parallax gear" renewal, no
// recorder call (so no session churn).
func TestParallaxGear_ReplayDuringChargeIsIgnored(t *testing.T) {
	m := newGearMonitor(t)
	ctx := context.Background()
	d := gearEnum(t, "D")
	replayTs := time.Now().Add(-2 * time.Minute).UnixMilli() // 105 s old, like 20:18:47 seen at 20:20:32

	// First sighting after a restart is not a baseline we can trust:
	// too old to apply.
	fresh := m.noteParallaxFrame("vid-1", rvmDriveGear, replayTs)
	m.applyParallaxGear(ctx, "vid-1", DriveDynamicsFrame{RVM: rvmDriveGear, Value: d, TimestampMs: replayTs}, fresh, "P")
	if g := m.cache["vid-1"].Gear; g != "P" {
		t.Fatalf("a stale D was applied: gear = %q", g)
	}
	if g, at := m.lastParallaxGear("vid-1"); g != "" || !at.IsZero() {
		t.Fatalf("a stale D renewed the Parallax gear veto: %q %v", g, at)
	}

	// Every later resubscribe replays the same ts_ms: not fresh.
	for i := 0; i < 5; i++ {
		fresh := m.noteParallaxFrame("vid-1", rvmDriveGear, replayTs)
		if fresh {
			t.Fatalf("replay %d reported fresh", i)
		}
		m.applyParallaxGear(ctx, "vid-1", DriveDynamicsFrame{RVM: rvmDriveGear, Value: d, TimestampMs: replayTs}, fresh, "P")
	}
	if s := m.sessions["vid-1"]; s != nil && (s.drive != nil || s.charge != nil) {
		t.Fatalf("replays touched the recorder: drive=%v charge=%v", s.drive, s.charge)
	}
	if g := m.cache["vid-1"].Gear; g != "P" {
		t.Fatalf("gear = %q after replays", g)
	}
}

// A real, new D (fresh ts) is still applied and still opens the drive.
func TestParallaxGear_FreshDrivingFrameStillApplies(t *testing.T) {
	m := newGearMonitor(t)
	m.cache["vid-1"].ChargerState = "charger_disconnected" // unplugged: a real drive-away
	m.cache["vid-1"].ChargerStatus = "chrgr_sts_not_connected"
	ctx := context.Background()
	d := gearEnum(t, "D")
	ts := time.Now().Add(-2 * time.Second).UnixMilli()
	fresh := m.noteParallaxFrame("vid-1", rvmDriveGear, ts)
	if !fresh {
		t.Fatal("first frame with a recent ts must be fresh")
	}
	m.applyParallaxGear(ctx, "vid-1", DriveDynamicsFrame{RVM: rvmDriveGear, Value: d, TimestampMs: ts}, fresh, "P")
	if g := m.cache["vid-1"].Gear; g != "D" {
		t.Fatalf("a fresh D was not applied: gear = %q", g)
	}
	if g, at := m.lastParallaxGear("vid-1"); g != "D" || at.IsZero() {
		t.Fatalf("Parallax gear not recorded: %q %v", g, at)
	}
	// And a newer P later is fresh too (ts advances).
	ts2 := time.Now().UnixMilli() + 1
	if !m.noteParallaxFrame("vid-1", rvmDriveGear, ts2) {
		t.Fatal("an advancing ts must be fresh")
	}
}

// A fresh frame must still be rejected when its event time is old, even
// when it is the first the monitor has seen (restart, no baseline).
func TestParallaxGear_FirstFrameAfterRestartNeedsRecentTimestamp(t *testing.T) {
	m := newGearMonitor(t)
	d := gearEnum(t, "D")
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	fresh := m.noteParallaxFrame("vid-1", rvmDriveGear, old)
	if !fresh {
		t.Fatal("first sighting is 'fresh' by ts-advance; the age gate must reject it")
	}
	m.applyParallaxGear(context.Background(), "vid-1", DriveDynamicsFrame{RVM: rvmDriveGear, Value: d, TimestampMs: old}, fresh, "P")
	if g := m.cache["vid-1"].Gear; g != "P" {
		t.Fatalf("10-minute-old D applied: %q", g)
	}
}

func TestNoteParallaxFrame_TsMsZeroIsNeverFresh(t *testing.T) {
	m := newGearMonitor(t)
	if m.noteParallaxFrame("vid-1", rvmDriveGear, 0) {
		t.Fatal("ts_ms <= 0 must not count as a fresh frame")
	}
}

// The ts_ms gate on its own (inside the age window): the first frame is
// applied, an identical replay of it (same ts, e.g. after a resubscribe)
// must not be applied again once vehicleState has moved on.
func TestParallaxGear_SameTimestampReplayIsNotReapplied(t *testing.T) {
	m := newGearMonitor(t)
	m.cache["vid-1"].ChargerState = "charger_disconnected"
	m.cache["vid-1"].ChargerStatus = "chrgr_sts_not_connected"
	ctx := context.Background()
	d := gearEnum(t, "D")
	ts := time.Now().Add(-5 * time.Second).UnixMilli()

	fresh := m.noteParallaxFrame("vid-1", rvmDriveGear, ts)
	m.applyParallaxGear(ctx, "vid-1", DriveDynamicsFrame{RVM: rvmDriveGear, Value: d, TimestampMs: ts}, fresh, "P")
	if g := m.cache["vid-1"].Gear; g != "D" {
		t.Fatalf("setup: first frame not applied: %q", g)
	}
	// vehicleState reports the park a moment later.
	m.mu.Lock()
	next := *m.cache["vid-1"]
	next.Gear = "P"
	m.cache["vid-1"] = &next
	m.mu.Unlock()
	_, atBefore := m.lastParallaxGear("vid-1")

	fresh = m.noteParallaxFrame("vid-1", rvmDriveGear, ts) // the replay: same ts_ms
	if fresh {
		t.Fatal("a same-ts replay reported fresh")
	}
	m.applyParallaxGear(ctx, "vid-1", DriveDynamicsFrame{RVM: rvmDriveGear, Value: d, TimestampMs: ts}, fresh, "P")
	if g := m.cache["vid-1"].Gear; g != "P" {
		t.Fatalf("replay re-applied D over vehicleState's P: %q", g)
	}
	if _, at := m.lastParallaxGear("vid-1"); !at.Equal(atBefore) {
		t.Fatal("replay renewed lastPxGearAt")
	}
}

type replayCounter struct{ byTopic map[string]int }

func (c *replayCounter) OnSleepEnded(string, time.Duration, bool) {}
func (c *replayCounter) OnParallaxReplayIgnored(topic string)     { c.byTopic[topic]++ }

// Power state and drive mode follow the same rule as the gear: a
// replayed "ready" must not read as a wake of a sleeping car (sleep
// gate, rest_quiet), nor a replayed drive mode overwrite the current
// one. Ignored replays are counted by topic.
func TestParallaxPowerAndDriveModeReplaysIgnored(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	m.parallaxDriveDynamics = true
	obs := &replayCounter{byTopic: map[string]int{}}
	m.SetSleepObserver(obs)
	m.cache["vid-1"] = &State{VehicleID: "vid-1", At: time.Now(), Gear: "P", PowerState: "sleep", DriveMode: "everyday",
		BatteryLevelPct: 60, OdometerKm: 1000}
	ctx := context.Background()
	old := time.Now().Add(-20 * time.Minute).UnixMilli()
	frame := func(rvm string, v uint64, ts int64) DriveDynamicsFrame {
		return DriveDynamicsFrame{RVM: rvm, Value: v, ValueOK: true, TimestampMs: ts}
	}
	cached := func() State {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return *m.cache["vid-1"]
	}

	// A resubscribe replays the last snapshots: "ready" and sport, both
	// from 20 minutes ago (first seen after a restart, so no baseline).
	m.onDriveDynamicsFrame(ctx, "vid-1", frame(rvmPowerState, 3, old))
	m.onDriveDynamicsFrame(ctx, "vid-1", frame(rvmDriveMode, 4, old))
	if st := cached(); st.PowerState != "sleep" || st.DriveMode != "everyday" {
		t.Errorf("replays applied: power %q, mode %q", st.PowerState, st.DriveMode)
	}
	// A real wake, now, applies; its replay (same ts) is ignored.
	now := time.Now().UnixMilli()
	m.onDriveDynamicsFrame(ctx, "vid-1", frame(rvmPowerState, 3, now))
	if st := cached(); st.PowerState != "ready" {
		t.Errorf("a fresh power state wasn't applied: %q", st.PowerState)
	}
	m.mu.Lock()
	m.cache["vid-1"].PowerState = "sleep" // back asleep (vehicleState)
	m.mu.Unlock()
	m.onDriveDynamicsFrame(ctx, "vid-1", frame(rvmPowerState, 3, now))
	if st := cached(); st.PowerState != "sleep" {
		t.Errorf("a replayed ready woke the car again: %q", st.PowerState)
	}
	if obs.byTopic[rvmPowerState] != 2 || obs.byTopic[rvmDriveMode] != 1 {
		t.Errorf("replays counted %v, want power 2, mode 1", obs.byTopic)
	}
}
