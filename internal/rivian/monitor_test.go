package rivian

import (
	"testing"
	"time"
)

// TestParseGNSSFixTime: empty/garbage GPS timestamp returns the zero
// time so callers can distinguish "no fix" from "fix at unix epoch".
// parseTimeOrNow's old behavior of substituting time.Now() for empty
// input was the v0.17.6 fragmentation incident.
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
