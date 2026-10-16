package flags

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMoneyTabState_Enabled(t *testing.T) {
	const a, b = "206f57ae-1047-565a-a5f7-b49718836f74", "06482049-f8c4-50c9-8d0d-4c3a498befe1"
	cases := []struct {
		name string
		st   MoneyTabState
		uid  string
		want bool
	}{
		{"default off", MoneyTabState{}, a, false},
		{"listed user", MoneyTabState{Users: []string{a}}, a, true},
		{"listed case-insensitive", MoneyTabState{Users: []string{" 206F57AE-1047-565A-A5F7-B49718836F74 "}}, a, true},
		{"other user", MoneyTabState{Users: []string{a}}, b, false},
		{"all", MoneyTabState{All: true}, b, true},
		{"empty uid", MoneyTabState{Users: []string{""}}, "", false},
	}
	for _, c := range cases {
		if got := c.st.Enabled(c.uid); got != c.want {
			t.Errorf("%s: Enabled(%q) = %v, want %v", c.name, c.uid, got, c.want)
		}
	}
	var nilStore *Store
	if nilStore.MoneyTab().Enabled(a) {
		t.Error("nil store must keep the tab off")
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
	if err := json.Unmarshal([]byte(`{"rest_quiet":{"vehicles":["01-2772711101","01-2781829100","01-242521064"]}}`), &w); err != nil {
		t.Fatal(err)
	}
	if !w.RestQuiet.Covers("01-242521064") || w.RestQuiet.Covers("01-248088167") || w.HoldAsleep.Covers("01-242521064") {
		t.Errorf("experiment A selection decoded as %+v", w)
	}
}

// ui_theme_next uses the money-tab shape: users, all, and off by default.
func TestUIThemeNextShapeAndDefaultOff(t *testing.T) {
	var none *Store
	if none.UIThemeNext().Enabled("u1") {
		t.Error("nil store: theme on")
	}
	if (&Store{}).UIThemeNext().Enabled("u1") {
		t.Error("empty store: theme on")
	}
	var users, all, junk MoneyTabState
	if err := json.Unmarshal([]byte(`{"users":["u1"]}`), &users); err != nil || !users.Enabled("u1") || users.Enabled("u2") {
		t.Errorf("users shape: %+v %v", users, err)
	}
	if err := json.Unmarshal([]byte(`{"all":true}`), &all); err != nil || !all.Enabled("anyone") {
		t.Errorf("all shape: %+v %v", all, err)
	}
	if json.Unmarshal([]byte(`"yes"`), &junk) == nil && junk.Enabled("u1") {
		t.Error("a malformed value turned the theme on")
	}
	if UIThemeNextName != "ui_theme_next" {
		t.Errorf("flag name = %q", UIThemeNextName)
	}
}
