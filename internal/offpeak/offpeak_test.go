package offpeak

import (
	"strings"
	"testing"
)

func win(days, start, end string, rate float64) Window {
	return Window{Days: days, Start: start, End: end, Rate: rate}
}

func TestNormalizeAccepts(t *testing.T) {
	good := Schedule{TZ: "America/Chicago", Windows: []Window{
		win(Weekdays, "21:00", "06:00", 0.0912345),
		win(Weekends, "06:00", "21:00", 0.07),
		win(Weekdays, "06:00", "21:00", 0.21),
	}}
	got, e := Normalize(good)
	if e != nil {
		t.Fatal(e)
	}
	if got.Windows[0].Rate != 0.0912 {
		t.Errorf("rate not rounded to 4 decimals: %v", got.Windows[0].Rate)
	}
	// Mon-Fri 21:00-06:00 reaches Saturday 06:00, so a weekend window from 00:00
	// would overlap it and is refused.
	clash := Schedule{TZ: "America/Chicago", Windows: []Window{good.Windows[0], win(Weekends, "00:00", "23:59", 0.07)}}
	if _, e := Normalize(clash); e == nil {
		t.Error("Friday night spilling into Saturday must overlap a weekend window from 00:00")
	}
	// An empty schedule clears, without needing a zone.
	if s, e := Normalize(Schedule{}); e != nil || len(s.Windows) != 0 || s.Windows == nil {
		t.Errorf("empty = %+v %v", s, e)
	}
}

func TestNormalizeRefuses(t *testing.T) {
	tz := "America/Chicago"
	nine := make([]Window, 9)
	for i := range nine {
		nine[i] = win(All, "00:00", "01:00", 0.1)
	}
	for _, c := range []struct {
		name  string
		s     Schedule
		field string
		msg   string
	}{
		{"no zone", Schedule{Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "IANA"},
		{"Local", Schedule{TZ: "Local", Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "IANA"},
		{"bad zone", Schedule{TZ: "Mars/Olympus", Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "IANA"},
		{"long zone", Schedule{TZ: strings.Repeat("a", 65), Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "IANA"},
		{"nine windows", Schedule{TZ: tz, Windows: nine}, "windows", "at most 8"},
		{"empty days", Schedule{TZ: tz, Windows: []Window{win("", "21:00", "06:00", 0.1)}}, "windows[0].days", "weekdays"},
		{"odd days", Schedule{TZ: tz, Windows: []Window{win("monday", "21:00", "06:00", 0.1)}}, "windows[0].days", "weekdays"},
		{"bad start", Schedule{TZ: tz, Windows: []Window{win(All, "9pm", "06:00", 0.1)}}, "windows[0].start", "21:00"},
		{"start 24:00", Schedule{TZ: tz, Windows: []Window{win(All, "24:00", "06:00", 0.1)}}, "windows[0].start", "21:00"},
		{"start 7:00", Schedule{TZ: tz, Windows: []Window{win(All, "7:00", "06:00", 0.1)}}, "windows[0].start", "21:00"},
		{"bad end", Schedule{TZ: tz, Windows: []Window{win(All, "21:00", "06:60", 0.1)}}, "windows[0].end", "06:00"},
		{"same start end", Schedule{TZ: tz, Windows: []Window{win(All, "21:00", "21:00", 0.1)}}, "windows[0].end", "differ"},
		{"negative rate", Schedule{TZ: tz, Windows: []Window{win(All, "21:00", "06:00", -0.01)}}, "windows[0].rate", "between 0 and 5"},
		{"rate over 5", Schedule{TZ: tz, Windows: []Window{win(All, "21:00", "06:00", 5.01)}}, "windows[0].rate", "between 0 and 5"},
		{"label markup", Schedule{TZ: tz, Windows: []Window{{Days: All, Start: "21:00", End: "06:00", Rate: 0.1, Label: "<b>x</b>"}}}, "windows[0].label", "not allowed"},
		{"label long", Schedule{TZ: tz, Windows: []Window{{Days: All, Start: "21:00", End: "06:00", Rate: 0.1, Label: strings.Repeat("x", 41)}}}, "windows[0].label", "longer than 40"},
		{"same day overlap", Schedule{TZ: tz, Windows: []Window{win(Weekdays, "06:00", "12:00", 0.2), win(Weekdays, "11:59", "13:00", 0.1)}}, "windows[1].start", "overlaps window 1"},
		{"all overlaps weekdays", Schedule{TZ: tz, Windows: []Window{win(Weekdays, "06:00", "12:00", 0.2), win(All, "10:00", "11:00", 0.1)}}, "windows[1].start", "overlaps window 1"},
		{"midnight spill overlaps next day", Schedule{TZ: tz, Windows: []Window{win(Weekdays, "22:00", "02:00", 0.07), win(Weekends, "01:00", "05:00", 0.09)}}, "windows[1].start", "overlaps window 1"},
		{"sunday night into monday", Schedule{TZ: tz, Windows: []Window{win(Weekends, "22:00", "03:00", 0.07), win(Weekdays, "02:00", "04:00", 0.09)}}, "windows[1].start", "overlaps window 1"},
	} {
		_, e := Normalize(c.s)
		if e == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if e.Field != c.field || !strings.Contains(e.Msg, c.msg) {
			t.Errorf("%s: %q / %q, want field %q containing %q", c.name, e.Field, e.Msg, c.field, c.msg)
		}
	}
}

func TestAdjacentWindowsAndWeekendSplit(t *testing.T) {
	// Touching windows do not overlap (end is exclusive); weekday and weekend
	// windows of the same hours are separate day types.
	s := Schedule{TZ: "America/New_York", Windows: []Window{
		win(Weekdays, "06:00", "14:00", 0.12),
		win(Weekdays, "14:00", "20:00", 0.30),
		win(Weekdays, "20:00", "06:00", 0.08),
		win(Weekends, "06:00", "20:00", 0.10),
	}}
	// Weekday night 20:00-06:00 spills into Saturday 00:00-06:00 and Sunday night
	// into Monday: the weekend window must not claim 00:00-06:00 on Saturday.
	if _, e := Normalize(s); e != nil {
		t.Fatalf("a valid weekday/weekend split was refused: %v", e)
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "06:30": 390, "23:59": 1439} {
		if got, ok := ParseClock(in); !ok || got != want {
			t.Errorf("%s = %d %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "6:30", "06:3", "06-30", "+6:30", "06:-1", "24:00", "ab:cd", "06:30:00"} {
		if _, ok := ParseClock(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
