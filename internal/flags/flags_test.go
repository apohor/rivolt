package flags

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAudienceState_Enabled(t *testing.T) {
	const a, b = "206f57ae-1047-565a-a5f7-b49718836f74", "06482049-f8c4-50c9-8d0d-4c3a498befe1"
	cases := []struct {
		name string
		st   AudienceState
		uid  string
		want bool
	}{
		{"default off", AudienceState{}, a, false},
		{"listed user", AudienceState{Users: []string{a}}, a, true},
		{"listed case-insensitive", AudienceState{Users: []string{" 206F57AE-1047-565A-A5F7-B49718836F74 "}}, a, true},
		{"other user", AudienceState{Users: []string{a}}, b, false},
		{"all", AudienceState{All: true}, b, true},
		{"empty uid", AudienceState{Users: []string{""}}, "", false},
	}
	for _, c := range cases {
		if got := c.st.Enabled(c.uid); got != c.want {
			t.Errorf("%s: Enabled(%q) = %v, want %v", c.name, c.uid, got, c.want)
		}
	}
	var nilStore *Store
	if nilStore.StatementEmail().Enabled(a) {
		t.Error("nil store must keep the feature off")
	}
}

func TestVehicleSetCovers(t *testing.T) {
	for _, tc := range []struct {
		set  VehicleSet
		id   string
		want bool
	}{
		{VehicleSet{}, "a", false},
		{VehicleSet{All: true}, "a", true},
		{VehicleSet{Vehicles: []string{"a"}}, "a", true},
		{VehicleSet{Vehicles: []string{"a"}}, "b", false},
		{VehicleSet{All: true, Except: []string{"a"}}, "a", false},
		{VehicleSet{All: true, Except: []string{"a"}}, "b", true},
		{VehicleSet{Vehicles: []string{"a"}, Except: []string{"a"}}, "a", false},
	} {
		if got := tc.set.Covers(tc.id); got != tc.want {
			t.Errorf("%+v.Covers(%q) = %v", tc.set, tc.id, got)
		}
	}
	if (SleepSafeState{All: true, Except: []string{"ctl"}}).Covers("ctl") {
		t.Error("sleep_safe except must opt a control vehicle out")
	}
}

func TestWakeSafetyDurations(t *testing.T) {
	for _, tc := range []struct {
		every, max         string
		wantEvery, wantMax time.Duration
	}{
		{"", "", DefaultHoldCheckEvery, DefaultHoldMax},
		{"bogus", "-1m", DefaultHoldCheckEvery, DefaultHoldMax},
		{"10s", "1m", time.Minute, 5 * time.Minute},
		{"1h", "24h", 15 * time.Minute, 2 * time.Hour},
		{"7m", "45m", 7 * time.Minute, 45 * time.Minute},
	} {
		w := WakeSafetyState{HoldCheckEvery: tc.every, HoldMax: tc.max}
		if w.CheckEvery() != tc.wantEvery || w.MaxHold() != tc.wantMax {
			t.Errorf("(%q, %q) → %v, %v", tc.every, tc.max, w.CheckEvery(), w.MaxHold())
		}
	}
}

func TestWakeSafetyJSON(t *testing.T) {
	var w WakeSafetyState
	raw := `{"capture_off":{"all":true,"except":["ctl"]},"hold_asleep":{"vehicles":["t1"]},"hold_check_every":"5m"}`
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	if !w.CaptureOff.Covers("t1") || w.CaptureOff.Covers("ctl") || !w.HoldAsleep.Covers("t1") || w.HoldAsleep.Covers("ctl") {
		t.Errorf("decoded %+v", w)
	}
	var nilStore *Store
	if w := nilStore.WakeSafety(); w.CaptureOff.All || w.HoldAsleep.All || len(w.HoldAsleep.Vehicles) > 0 {
		t.Error("nil store must read all-off")
	}
}

func TestWakeSafetyRestQuietJSON(t *testing.T) {
	var w WakeSafetyState
	if err := json.Unmarshal([]byte(`{"rest_quiet":{"vehicles":["01-000000005","01-000000002","01-000000000"]}}`), &w); err != nil {
		t.Fatal(err)
	}
	if !w.RestQuiet.Covers("01-000000000") || w.RestQuiet.Covers("01-000000003") || w.HoldAsleep.Covers("01-000000000") {
		t.Errorf("experiment A selection decoded as %+v", w)
	}
}
