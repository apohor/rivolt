package flags

import "testing"

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
