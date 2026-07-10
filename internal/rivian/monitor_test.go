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

// TestMaybeStripGPS asserts the Parallax-GPS flag governs whether a
// vehicleState snapshot's GPS survives into the cache: stripped when
// Parallax is authoritative, untouched otherwise. Non-GPS fields are
// never altered.
func TestMaybeStripGPS(t *testing.T) {
	sample := func() *State {
		return &State{
			Latitude: 30.55, Longitude: -97.76, SpeedKph: 88,
			HeadingDeg: 344.8, AltitudeM: 240, LocationFixAt: time.Unix(1, 0),
			BatteryLevelPct: 72, Gear: "D",
		}
	}

	// Resolved-on for this vehicle: GPS zeroed, everything else kept.
	on := &StateMonitor{parallaxGPSFor: map[string]bool{"v1": true}}
	st := sample()
	on.maybeStripGPS("v1", st)
	if st.Latitude != 0 || st.Longitude != 0 || st.SpeedKph != 0 ||
		st.HeadingDeg != 0 || st.AltitudeM != 0 || !st.LocationFixAt.IsZero() {
		t.Fatalf("resolved on: GPS not fully stripped: %+v", st)
	}
	if st.BatteryLevelPct != 72 || st.Gear != "D" {
		t.Fatalf("resolved on: non-GPS fields altered: %+v", st)
	}

	// A different vehicle (not resolved on) is untouched even on the
	// same monitor — the gate is per-vehicle.
	st2 := sample()
	on.maybeStripGPS("other", st2)
	if st2.Latitude != 30.55 || st2.SpeedKph != 88 || st2.LocationFixAt.IsZero() {
		t.Fatalf("other vehicle: GPS unexpectedly modified: %+v", st2)
	}

	// Resolved-off: snapshot untouched.
	off := &StateMonitor{parallaxGPSFor: map[string]bool{"v1": false}}
	st3 := sample()
	off.maybeStripGPS("v1", st3)
	if st3.Latitude != 30.55 || st3.SpeedKph != 88 || st3.LocationFixAt.IsZero() {
		t.Fatalf("resolved off: GPS unexpectedly modified: %+v", st3)
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
