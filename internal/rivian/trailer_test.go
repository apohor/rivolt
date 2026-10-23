package rivian

import (
	"context"
	"testing"
	"time"
)

var tt0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func flag(v *bool) string {
	if v == nil {
		return "nil"
	}
	if *v {
		return "true"
	}
	return "false"
}

// The drive's flag is the share of the time the trailer state was known during which the connector read present, with
// the last known value carried forward (the topic may send only on change).
func TestTrailerAccCarriesTheLastKnownValueForward(t *testing.T) {
	min := func(n int) time.Time { return tt0.Add(time.Duration(n) * time.Minute) }
	for _, c := range []struct {
		name string
		run  func(a *trailerAcc)
		end  int
		want string
	}{
		{"never known", func(a *trailerAcc) { a.observe(min(0), ""); a.observe(min(20), "") }, 30, "nil"},
		{"a single present frame at the start, then silence", func(a *trailerAcc) { a.observe(min(0), TrailerPresent); a.observe(min(10), ""); a.observe(min(20), "") }, 30, "true"},
		{"a single absent frame at the start, then silence", func(a *trailerAcc) { a.observe(min(0), TrailerAbsent); a.observe(min(15), "") }, 30, "false"},
		{"unhitched a third of the way in (present 10 of 30)", func(a *trailerAcc) { a.observe(min(0), TrailerPresent); a.observe(min(10), TrailerAbsent) }, 30, "false"},
		{"unhitched two thirds in (present 20 of 30)", func(a *trailerAcc) { a.observe(min(0), TrailerPresent); a.observe(min(20), TrailerAbsent) }, 30, "true"},
		{"exactly half present counts", func(a *trailerAcc) { a.observe(min(0), TrailerPresent); a.observe(min(15), TrailerAbsent) }, 30, "true"},
		{"known only late (present for the last 5 of 30): time before it is not counted", func(a *trailerAcc) { a.observe(min(0), ""); a.observe(min(25), TrailerPresent) }, 30, "true"},
		{"known late and absent", func(a *trailerAcc) { a.observe(min(0), ""); a.observe(min(25), TrailerAbsent) }, 30, "false"},
		{"a value only at the very end", func(a *trailerAcc) { a.observe(min(30), TrailerPresent) }, 30, "true"},
		{"a time that goes backwards changes nothing", func(a *trailerAcc) {
			a.observe(min(10), TrailerPresent)
			a.observe(min(5), TrailerAbsent)
			a.observe(min(20), "")
		}, 30, "false"},
	} {
		var a trailerAcc
		c.run(&a)
		if got := flag(a.result(min(c.end))); got != c.want {
			t.Errorf("%s: %s, want %s (present %v of known %v)", c.name, got, c.want, a.present, a.known)
		}
	}
	// A mid-drive snapshot does not change the accumulator, so the final one agrees with an uninterrupted run.
	var a trailerAcc
	a.observe(tt0, TrailerPresent)
	_ = a.result(tt0.Add(10 * time.Minute))
	a.observe(tt0.Add(20*time.Minute), TrailerAbsent)
	if got := flag(a.result(tt0.Add(30 * time.Minute))); got != "true" {
		t.Errorf("after a snapshot: %s, want true (present 20 of 30)", got)
	}
}

func TestTrailerStateFromParallax(t *testing.T) {
	for v, want := range map[uint64]string{0: "", 1: TrailerAbsent, 2: TrailerPresent, 3: TrailerPresent, 4: "", 9: ""} {
		if got := trailerStateFromParallax(v); got != want {
			t.Errorf("enum %d = %q, want %q", v, got, want)
		}
	}
}

func trailerMonitor() *StateMonitor {
	m := NewStateMonitor(nil, nil)
	m.parallaxDriveDynamics = true
	m.vehicleInfo["v"] = &Vehicle{ID: "v", Model: "R1S"}
	return m
}

func (m *StateMonitor) trailerNow() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c := m.cache["v"]; c != nil {
		return c.TrailerState
	}
	return "(no state)"
}

// A fresh frame sets the state; a replayed or stale one (the cloud re-sends the last snapshot on every resubscribe)
// only fills a blank; "invalid" and "unspecified" change nothing; present with brakes is present.
func TestTrailerStateFromFramesFreshReplayedAndInvalid(t *testing.T) {
	ctx := context.Background()
	m := trailerMonitor()
	m.applyVehicleStatePush(ctx, "v", &State{VehicleID: "v", At: time.Now(), Gear: "P", BatteryLevelPct: 60, OdometerKm: 30000})
	frame := func(enum uint64, ts time.Time) {
		m.onDriveDynamicsFrame(ctx, "v", DriveDynamicsFrame{RVM: rvmTrailer, Value: enum, ValueOK: true, TimestampMs: ts.UnixMilli()})
	}
	// A stale frame (old event time) fills a blank state.
	frame(2, time.Now().Add(-time.Hour))
	if got := m.trailerNow(); got != TrailerPresent {
		t.Fatalf("a stale frame did not fill the blank: %q", got)
	}
	// A fresh "not present" replaces it.
	frame(1, time.Now())
	if got := m.trailerNow(); got != TrailerAbsent {
		t.Fatalf("fresh not_present: %q", got)
	}
	// A stale "present" (a replay) must not overwrite what is known.
	frame(2, time.Now().Add(-time.Hour))
	if got := m.trailerNow(); got != TrailerAbsent {
		t.Fatalf("a replayed present overwrote a known state: %q", got)
	}
	// Invalid and unspecified leave it; present with brakes is present.
	frame(4, time.Now().Add(time.Second))
	frame(0, time.Now().Add(2*time.Second))
	if got := m.trailerNow(); got != TrailerAbsent {
		t.Fatalf("invalid/unspecified changed it: %q", got)
	}
	frame(3, time.Now().Add(3*time.Second))
	if got := m.trailerNow(); got != TrailerPresent {
		t.Fatalf("present with brakes: %q", got)
	}
	// A vehicleState push (no trailer in it) keeps the last known value.
	m.applyVehicleStatePush(ctx, "v", &State{VehicleID: "v", At: time.Now(), Gear: "P", BatteryLevelPct: 59, OdometerKm: 30000})
	if got := m.trailerNow(); got != TrailerPresent {
		t.Fatalf("a push dropped the trailer state: %q", got)
	}
}

// A drive records the flag: hitched before it starts and silent after (a topic that sends only on change), unhitched
// part-way, and never known.
func TestADriveRecordsTheTrailerFlag(t *testing.T) {
	ctx := context.Background()
	drive := func(setup func(m *StateMonitor), mid func(m *StateMonitor, step int)) *bool {
		m := trailerMonitor()
		base := time.Now().Add(-time.Hour)
		push := func(i int, gear string, mph float64) {
			m.applyVehicleStatePush(ctx, "v", &State{VehicleID: "v", At: base.Add(time.Duration(i) * time.Minute), Gear: gear,
				SpeedKph: mph / kphToMi, BatteryLevelPct: 80 - float64(i)/10, OdometerKm: 30000 + float64(i)})
		}
		push(0, "P", 0)
		if setup != nil {
			setup(m)
		}
		for i := 1; i <= 20; i++ {
			if mid != nil {
				mid(m, i)
			}
			push(i, "D", 45)
		}
		m.sessMu.Lock()
		var d *liveDrive
		if sess := m.sessions["v"]; sess != nil {
			d = sess.drive
		}
		m.sessMu.Unlock()
		if d == nil {
			t.Fatal("no drive")
		}
		return m.liveDriveRow("v", d).Trailer
	}
	// Every frame gets its own event time: one with a time already seen is the cloud's replay of it.
	var seq int64
	px := func(m *StateMonitor, enum uint64) {
		seq++
		m.onDriveDynamicsFrame(ctx, "v", DriveDynamicsFrame{RVM: rvmTrailer, Value: enum, ValueOK: true, TimestampMs: time.Now().UnixMilli() + seq})
	}
	if got := flag(drive(func(m *StateMonitor) { px(m, 2) }, nil)); got != "true" {
		t.Errorf("hitched before the drive, then silence: %s, want true", got)
	}
	if got := flag(drive(func(m *StateMonitor) { px(m, 1) }, nil)); got != "false" {
		t.Errorf("no trailer, known at the start: %s, want false", got)
	}
	if got := flag(drive(nil, nil)); got != "nil" {
		t.Errorf("never known: %s, want nil", got)
	}
	// Unhitched at minute 15 of a 21-minute drive: present about 15 of 21 minutes, so a trailer drive.
	if got := flag(drive(func(m *StateMonitor) { px(m, 2) }, func(m *StateMonitor, i int) {
		if i == 15 {
			px(m, 1)
		}
	})); got != "true" {
		t.Errorf("unhitched late: %s, want true", got)
	}
	// Unhitched at minute 5: present about 5 of 21 minutes, so not.
	if got := flag(drive(func(m *StateMonitor) { px(m, 2) }, func(m *StateMonitor, i int) {
		if i == 5 {
			px(m, 1)
		}
	})); got != "false" {
		t.Errorf("unhitched early: %s, want false", got)
	}
}

// The restart snapshot carries the integral.
func TestTheTrailerIntegralSurvivesARestartSnapshot(t *testing.T) {
	s := &liveSessions{drive: &liveDrive{id: "d", startedAt: tt0, endAt: tt0.Add(10 * time.Minute)}}
	s.drive.trailer.observe(tt0, TrailerPresent)
	s.drive.trailer.observe(tt0.Add(10*time.Minute), "")
	snap := s.snapshot()
	back := liveSessionsFromSnapshot(snap)
	if back.drive == nil || back.drive.trailer != s.drive.trailer {
		t.Errorf("restored %+v, want %+v", back.drive.trailer, s.drive.trailer)
	}
}
