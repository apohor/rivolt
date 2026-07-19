package rivian

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// waitReauthClear parks while the client is flagged needs_reauth and
// resumes once the flag clears — instead of the old behavior of
// resubscribing into a 10-minute zombie WS every cycle.
func TestWaitReauthClearParksUntilCleared(t *testing.T) {
	old := reauthPollInterval
	reauthPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { reauthPollInterval = old })

	c := NewLive()
	c.SetNeedsReauth(true, "session expired")
	m := &StateMonitor{
		client: c,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		active: map[string]context.CancelFunc{},
	}

	done := make(chan bool, 1)
	go func() { done <- m.waitReauthClear(context.Background(), "veh-1") }()

	select {
	case <-done:
		t.Fatal("waitReauthClear returned while still flagged; should park")
	case <-time.After(40 * time.Millisecond):
	}

	c.SetNeedsReauth(false, "")
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitReauthClear returned false after the flag cleared, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("waitReauthClear did not resume within 1s of the flag clearing")
	}
}

// A cancelled context (monitor shutdown) unparks and returns false.
func TestWaitReauthClearHonorsContextCancel(t *testing.T) {
	old := reauthPollInterval
	reauthPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { reauthPollInterval = old })

	c := NewLive()
	c.SetNeedsReauth(true, "session expired")
	m := &StateMonitor{
		client: c,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		active: map[string]context.CancelFunc{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.waitReauthClear(ctx, "veh-1") {
		t.Fatal("waitReauthClear returned true on a cancelled context, want false")
	}
}

// TestParseGNSSFixTime: empty/garbage GPS timestamp returns the zero
// time so callers can distinguish "no fix" from "fix at unix epoch".
// parseTimeOrNow's old behavior of substituting time.Now() for empty
// input fragmented trips into 3-min stubs whenever a frame carried
// a stale GNSS timestamp.
func TestParseGNSSFixTime(t *testing.T) {
	if got := parseGNSSFixTime(""); !got.IsZero() {
		t.Errorf("empty input must return zero time, got %v", got)
	}
	if got := parseGNSSFixTime("not a timestamp"); !got.IsZero() {
		t.Errorf("garbage input must return zero time, got %v", got)
	}
	want := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	if got := parseGNSSFixTime("2026-05-01T12:30:00Z"); !got.Equal(want) {
		t.Errorf("RFC3339 input: got %v want %v", got, want)
	}
}

// TestMergeStatePreservesLocationFixAt: a push delta without a
// GNSSLocation block (fixAt zero) must NOT overwrite a known-good
// prior fix in the cache.
func TestMergeStatePreservesLocationFixAt(t *testing.T) {
	prev := &State{LocationFixAt: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)}
	next := &State{} // delta without GNSSLocation
	merged := mergeState(prev, next)
	if !merged.LocationFixAt.Equal(prev.LocationFixAt) {
		t.Errorf("zero LocationFixAt must not overwrite prior; got %v want %v",
			merged.LocationFixAt, prev.LocationFixAt)
	}
	// Newer non-zero replaces older.
	newer := time.Date(2026, 5, 4, 13, 0, 0, 0, time.UTC)
	merged2 := mergeState(prev, &State{LocationFixAt: newer})
	if !merged2.LocationFixAt.Equal(newer) {
		t.Errorf("non-zero LocationFixAt must replace prior; got %v want %v",
			merged2.LocationFixAt, newer)
	}
}

// TestWakeWorthyTransition exercises the heuristic used by
// periodicRefresh to decide when a fresh REST snapshot warrants
// kicking the WS resubscribe loop. Bias is toward false negatives:
// a missed nudge just means waiting for the watchdog window;
// over-firing wakes a sleeping car for no reason.
func TestWakeWorthyTransition(t *testing.T) {
	type frame struct {
		ps, gear, cs string
		soc          float64
	}
	mk := func(f frame) *State {
		return &State{
			PowerState:      f.ps,
			Gear:            f.gear,
			ChargerState:    f.cs,
			BatteryLevelPct: f.soc,
		}
	}
	cases := []struct {
		name      string
		prev, cur frame
		want      bool
	}{
		{"sleep_to_ready", frame{ps: "sleep", soc: 50}, frame{ps: "ready", soc: 50}, true},
		{"empty_to_go", frame{ps: "", soc: 50}, frame{ps: "go", gear: "D", soc: 50}, true},
		{"park_to_drive", frame{ps: "ready", gear: "P", soc: 50}, frame{ps: "ready", gear: "D", soc: 50}, true},
		{"unplug_to_charging", frame{ps: "ready", cs: "chrgr_sts_not_connected", soc: 50}, frame{ps: "ready", cs: "charging_active", soc: 50}, true},
		{"large_soc_jump", frame{ps: "sleep", soc: 50}, frame{ps: "sleep", soc: 53}, true},
		{"sleep_stays_sleep", frame{ps: "sleep", soc: 50}, frame{ps: "sleep", soc: 50}, false},
		{"ready_stays_ready", frame{ps: "ready", soc: 50}, frame{ps: "ready", soc: 50.2}, false},
		{"drive_stays_drive", frame{ps: "go", gear: "D", soc: 50}, frame{ps: "go", gear: "D", soc: 49.5}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wakeWorthyTransition(mk(tc.prev), mk(tc.cur))
			if got != tc.want {
				t.Fatalf("wakeWorthyTransition(%+v -> %+v) = %v, want %v", tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}

// TestAdaptiveRefreshInterval pins the REST poll cadence. The key
// case: a connected-but-idle car (charging_ready / charging_complete)
// must NOT pull the 2-min active cadence — only a pack actually
// drawing power (charging_active / charging_connecting) does.
func TestAdaptiveRefreshInterval(t *testing.T) {
	const vid = "vid-1"
	cases := []struct {
		name   string
		wsSeen bool
		st     *State
		want   time.Duration
	}{
		{"cold_start_no_ws", false, &State{PowerState: "sleep"}, 2 * time.Minute},
		{"ws_seen_no_cache", true, nil, 30 * time.Minute},
		{"driving", true, &State{PowerState: "go"}, 2 * time.Minute},
		{"ready", true, &State{PowerState: "ready"}, 10 * time.Minute},
		{"standby", true, &State{PowerState: "standby"}, 10 * time.Minute},
		{"sleep_active_charge", true, &State{PowerState: "sleep", ChargerState: "charging_active"}, 2 * time.Minute},
		{"sleep_connecting", true, &State{PowerState: "sleep", ChargerState: "charging_connecting"}, 2 * time.Minute},
		{"sleep_charging_ready_idle", true, &State{PowerState: "sleep", ChargerState: "charging_ready"}, 30 * time.Minute},
		{"sleep_charging_complete", true, &State{PowerState: "sleep", ChargerState: "charging_complete"}, 30 * time.Minute},
		{"sleep_unplugged", true, &State{PowerState: "sleep", ChargerState: "chrgr_sts_not_connected"}, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &StateMonitor{
				cache:  map[string]*State{},
				wsSeen: map[string]bool{},
			}
			if tc.st != nil {
				m.cache[vid] = tc.st
			}
			m.wsSeen[vid] = tc.wsSeen
			if got := m.adaptiveRefreshInterval(vid); got != tc.want {
				t.Fatalf("adaptiveRefreshInterval(%+v) = %v, want %v", tc.st, got, tc.want)
			}
		})
	}
}

// TestNoteVehStateGPS asserts vehicleState GPS fixes are stamped only
// when the master is on and the frame actually carries a fix — the
// signal the gnss gap-filler uses to tell a live feed from a stalled one.
func TestNoteVehStateGPS(t *testing.T) {
	withFix := &State{Latitude: 30.55, Longitude: -97.76}
	noFix := &State{BatteryLevelPct: 72} // delta with no GPS

	// Master on + fix present → stamped.
	on := &StateMonitor{parallaxGPS: true, lastVehStateGPS: map[string]time.Time{}}
	on.noteVehStateGPS("v1", withFix)
	if on.lastVehStateGPS["v1"].IsZero() {
		t.Fatal("master on + fix: expected a stamp")
	}

	// A fixless frame doesn't count as a vehicleState GPS update.
	on.noteVehStateGPS("v2", noFix)
	if !on.lastVehStateGPS["v2"].IsZero() {
		t.Fatal("fixless frame should not stamp")
	}

	// Master off → never tracked (no overhead in prod).
	off := &StateMonitor{parallaxGPS: false, lastVehStateGPS: map[string]time.Time{}}
	off.noteVehStateGPS("v1", withFix)
	if !off.lastVehStateGPS["v1"].IsZero() {
		t.Fatal("master off: should not stamp")
	}
}

// TestParseBoolEnv pins the feature-flag truthiness table.
func TestParseBoolEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "Yes", "on"} {
		if !parseBoolEnv(v) {
			t.Errorf("parseBoolEnv(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "nope"} {
		if parseBoolEnv(v) {
			t.Errorf("parseBoolEnv(%q) = true, want false", v)
		}
	}
}

// noteSubEnd warns only after a burst of short WS sessions — the
// signature of a competing subscriber to the same vehicle repeatedly
// kicking our feed — and rate-limits the warning.
func TestNoteSubEnd_FlapWarningOnShortSessionBurst(t *testing.T) {
	m := &StateMonitor{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		shortSubEnds:   map[string][]time.Time{},
		lastFlapWarnAt: map[string]time.Time{},
	}
	const veh = "veh-1"
	short := flapSessionMax - time.Second

	// Under the threshold: no warning yet.
	for i := 0; i < flapCount-1; i++ {
		if m.noteSubEnd(veh, short, false) {
			t.Fatalf("warned after only %d short sessions, want >= %d", i+1, flapCount)
		}
	}
	// The flapCount-th short session crosses the threshold.
	if !m.noteSubEnd(veh, short, false) {
		t.Fatalf("no flap warning after %d short sessions", flapCount)
	}
	// Rate-limited: an immediate follow-up must NOT re-warn.
	if m.noteSubEnd(veh, short, false) {
		t.Fatal("flap warning re-fired within the cooldown window")
	}
}

// Nudge resubscribes (intentional fast bounces) and healthy long
// sessions must never count toward flapping, no matter how many.
func TestNoteSubEnd_IgnoresNudgesAndLongSessions(t *testing.T) {
	m := &StateMonitor{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		shortSubEnds:   map[string][]time.Time{},
		lastFlapWarnAt: map[string]time.Time{},
	}
	const veh = "veh-1"
	for i := 0; i < flapCount*3; i++ {
		if m.noteSubEnd(veh, time.Second, true) { // nudged short session
			t.Fatal("nudge resubscribe counted toward flapping")
		}
		if m.noteSubEnd(veh, flapSessionMax+time.Minute, false) { // healthy long session
			t.Fatal("healthy long session counted toward flapping")
		}
	}
	if got := len(m.shortSubEnds[veh]); got != 0 {
		t.Fatalf("shortSubEnds recorded %d entries for nudges/long sessions, want 0", got)
	}
}

// A short-session burst on one vehicle must not trip the warning for a
// different vehicle (per-vehicle windows are independent).
func TestNoteSubEnd_PerVehicleIsolation(t *testing.T) {
	m := &StateMonitor{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		shortSubEnds:   map[string][]time.Time{},
		lastFlapWarnAt: map[string]time.Time{},
	}
	short := flapSessionMax - time.Second
	for i := 0; i < flapCount+2; i++ {
		m.noteSubEnd("veh-A", short, false)
	}
	if m.noteSubEnd("veh-B", short, false) {
		t.Fatal("veh-B warned off veh-A's short sessions; windows should be per-vehicle")
	}
}
