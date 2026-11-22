package api

import (
	"testing"
	"time"
)

func TestPromptHidden(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	cases := []struct {
		name    string
		m       promptMark
		changed time.Time
		want    bool
	}{
		{"never dismissed", promptMark{}, time.Time{}, false},
		{"first Not now, yesterday", promptMark{now.Add(-day), 1}, time.Time{}, true},
		{"first Not now, 89 days ago", promptMark{now.Add(-89 * day), 1}, time.Time{}, true},
		{"first Not now, 91 days ago", promptMark{now.Add(-91 * day), 1}, time.Time{}, false},
		{"second Not now, long ago, stays hidden", promptMark{now.Add(-400 * day), 2}, time.Time{}, true},
		{"dismissed before a lapse, shown again", promptMark{now.Add(-day), 2}, now.Add(-time.Hour), false},
		{"dismissed after the lapse", promptMark{now.Add(-time.Hour), 1}, now.Add(-day), true},
	}
	for _, c := range cases {
		if got := promptHidden(c.m, c.changed, now); got != c.want {
			t.Errorf("%s: hidden = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPromptWhereList(t *testing.T) {
	if !validPromptWhere("planner") || !validPromptWhere("lapse_banner") {
		t.Fatal("known places must be valid")
	}
	for _, w := range []string{"", "Planner", "planner ", "../x", "admin"} {
		if validPromptWhere(w) {
			t.Errorf("%q must not be a place", w)
		}
	}
}

func TestParsePromptStateTolerant(t *testing.T) {
	for _, raw := range []string{"", "null", "{", "[1]", `"x"`} {
		if m := parsePromptState(raw); m == nil || len(m) != 0 {
			t.Errorf("%q: got %v, want empty map", raw, m)
		}
	}
}
