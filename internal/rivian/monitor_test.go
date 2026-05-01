package rivian

import "testing"

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
