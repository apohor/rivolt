package offpeak

import (
	"strings"
	"testing"
	"time"
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
		{"no zone", Schedule{Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "time zone"},
		{"Local", Schedule{TZ: "Local", Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "time zone"},
		{"bad zone", Schedule{TZ: "Mars/Olympus", Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "time zone"},
		{"long zone", Schedule{TZ: strings.Repeat("a", 65), Windows: []Window{win(All, "21:00", "06:00", 0.1)}}, "tz", "time zone"},
		{"nine windows", Schedule{TZ: tz, Windows: nine}, "windows", "at most 8 windows"},
		{"empty days", Schedule{TZ: tz, Windows: []Window{win("", "21:00", "06:00", 0.1)}}, "windows[0].days", "choose weekdays"},
		{"odd days", Schedule{TZ: tz, Windows: []Window{win("monday", "21:00", "06:00", 0.1)}}, "windows[0].days", "choose weekdays"},
		{"bad start", Schedule{TZ: tz, Windows: []Window{win(All, "9pm", "06:00", 0.1)}}, "windows[0].start", "start must be a time like 21:00"},
		{"start 24:00", Schedule{TZ: tz, Windows: []Window{win(All, "24:00", "06:00", 0.1)}}, "windows[0].start", "start must be a time like 21:00"},
		{"start 7:00", Schedule{TZ: tz, Windows: []Window{win(All, "7:00", "06:00", 0.1)}}, "windows[0].start", "start must be a time like 21:00"},
		{"bad end", Schedule{TZ: tz, Windows: []Window{win(All, "21:00", "06:60", 0.1)}}, "windows[0].end", "end must be a time like 06:00"},
		{"same start end", Schedule{TZ: tz, Windows: []Window{win(All, "21:00", "21:00", 0.1)}}, "windows[0].end", "must differ"},
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

// The messages are shown to the user as they are, so each names its window.
func TestMessagesArePlainWords(t *testing.T) {
	_, e := Normalize(Schedule{TZ: "America/Chicago", Windows: []Window{win(Weekdays, "06:00", "12:00", 0.2), win(All, "10:00", "11:00", 0.1)}})
	if e == nil || e.Msg != "Window 2 overlaps window 1." || e.Field != "windows[1].start" {
		t.Errorf("overlap = %+v", e)
	}
	_, e = Normalize(Schedule{TZ: "America/Chicago", Windows: []Window{win(All, "21:00", "06:00", 9)}})
	if e == nil || e.Msg != "Window 1: the rate must be between 0 and 5 per kWh." {
		t.Errorf("rate = %+v", e)
	}
	// An end before the start, and 00:00 as an end, are fine: past midnight.
	if _, e = Normalize(Schedule{TZ: "America/Chicago", Windows: []Window{win(All, "21:00", "00:00", 0.1)}}); e != nil {
		t.Errorf("a window ending at 00:00 was refused: %v", e)
	}
}

func TestInstances(t *testing.T) {
	s, e := Normalize(Schedule{TZ: "America/Chicago", Windows: []Window{
		win(Weekdays, "21:00", "06:00", 0.08),
		win(Weekends, "06:00", "20:00", 0.10),
	}})
	if e != nil {
		t.Fatal(e)
	}
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	// Wednesday 2026-10-21 02:00 to Thursday 08:00 holds Tuesday night's tail and
	// Wednesday night.
	got := s.Instances(at(2026, 10, 21, 2, 0), at(2026, 10, 22, 8, 0))
	if len(got) != 2 {
		t.Fatalf("%d instances, want Tuesday night's tail and Wednesday night: %+v", len(got), got)
	}
	if !got[0].End.Equal(at(2026, 10, 21, 6, 0)) || !got[1].Start.Equal(at(2026, 10, 21, 21, 0)) || !got[1].End.Equal(at(2026, 10, 22, 6, 0)) || got[1].Rate != 0.08 {
		t.Errorf("instances = %+v", got)
	}
	// Friday night runs into Saturday morning; Saturday's own window is 06:00-20:00.
	var spill, sat bool
	for _, in := range s.Instances(at(2026, 10, 23, 20, 0), at(2026, 10, 24, 12, 0)) {
		if in.Start.Equal(at(2026, 10, 23, 21, 0)) && in.End.Equal(at(2026, 10, 24, 6, 0)) {
			spill = true
		}
		if in.Start.Equal(at(2026, 10, 24, 6, 0)) && in.End.Equal(at(2026, 10, 24, 20, 0)) && in.Rate == 0.10 {
			sat = true
		}
	}
	if !spill || !sat {
		t.Errorf("Friday-night spill %v, Saturday window %v", spill, sat)
	}
	// Saturday and Sunday nights have no weekday night window.
	for _, in := range s.Instances(at(2026, 10, 24, 20, 0), at(2026, 10, 25, 23, 0)) {
		if in.Rate == 0.08 && !in.Start.Before(at(2026, 10, 24, 6, 0)) {
			t.Errorf("a weekday night window on a weekend night: %+v", in)
		}
	}
	// Fall back (2026-11-01, 02:00 CDT -> 01:00 CST): Friday night 21:00 to
	// Saturday 06:00 is nowhere near it, but Sunday's own 06:00-20:00 window is
	// anchored to wall-clock times on either side of the change.
	sun := s.Instances(at(2026, 11, 1, 5, 0), at(2026, 11, 1, 21, 0))
	if len(sun) != 1 || !sun[0].Start.Equal(at(2026, 11, 1, 6, 0)) || !sun[0].End.Equal(at(2026, 11, 1, 20, 0)) {
		t.Errorf("fall-back Sunday = %+v", sun)
	}
	// A long span is fine and an empty or reversed one is nothing.
	if n := len(s.Instances(at(2026, 10, 1, 0, 0), at(2026, 11, 1, 0, 0))); n < 30 {
		t.Errorf("a month has %d instances", n)
	}
	if s.Instances(at(2026, 10, 22, 0, 0), at(2026, 10, 22, 0, 0)) != nil {
		t.Error("an empty span has instances")
	}
}
