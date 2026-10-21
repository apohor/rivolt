// Package offpeak is the user's electricity schedule: the times of day (in
// their own time zone) at which their plan charges a different rate, and the
// checks that keep a schedule well formed. Rivolt only uses it for estimates; it
// never contacts a utility and cannot check the numbers.
package offpeak

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits of a schedule.
const (
	MaxWindows   = 8
	MaxRate      = 5.0
	MaxLabelLen  = 40
	MaxTZLen     = 64
	minutesInDay = 24 * 60
)

// Days of a window: Mon-Fri, Sat-Sun, or every day.
const (
	Weekdays = "weekdays"
	Weekends = "weekends"
	All      = "all"
)

// Window is one stretch of the day at one rate. Start and End are local
// wall-clock times ("21:00"); End at or before Start crosses midnight into the
// next day. Rate is in the user's home currency per kWh.
type Window struct {
	Days  string  `json:"days"`
	Start string  `json:"start"`
	End   string  `json:"end"`
	Rate  float64 `json:"rate"`
	Label string  `json:"label,omitempty"`
}

// Schedule is the user's windows and the IANA zone their times are read in.
// Hours outside every window are priced at the user's flat home rate.
type Schedule struct {
	TZ      string   `json:"tz"`
	Windows []Window `json:"windows"`
}

// FieldError is a validation failure with the path of the field at fault
// ("windows[2].end"), so a form can show it beside the right row.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"error"`
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

func bad(field, format string, a ...any) *FieldError {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

// Normalize checks s and returns it with rates rounded to 4 decimals. An empty
// schedule (no windows) is valid and clears the schedule.
func Normalize(s Schedule) (Schedule, *FieldError) {
	if len(s.Windows) == 0 {
		return Schedule{TZ: strings.TrimSpace(s.TZ), Windows: []Window{}}, nil
	}
	tz := strings.TrimSpace(s.TZ)
	if tz == "" || len(tz) > MaxTZLen || strings.EqualFold(tz, "local") {
		return s, bad("tz", "Choose your time zone, e.g. America/Chicago.")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return s, bad("tz", "Choose your time zone, e.g. America/Chicago.")
	}
	if len(s.Windows) > MaxWindows {
		return s, bad("windows", "You can add at most %d windows.", MaxWindows)
	}
	out := Schedule{TZ: tz, Windows: make([]Window, len(s.Windows))}
	// owner[day][minute] is 1+the window that holds the minute.
	var owner [7][minutesInDay]int8
	for i, w := range s.Windows {
		f := func(name string) string { return fmt.Sprintf("windows[%d].%s", i, name) }
		// Every message names its window, so a form can show it as it stands.
		n := i + 1
		switch w.Days {
		case Weekdays, Weekends, All:
		default:
			return s, bad(f("days"), "Window %d: choose weekdays, weekends or every day.", n)
		}
		start, ok := ParseClock(w.Start)
		if !ok {
			return s, bad(f("start"), "Window %d: the start must be a time like 21:00.", n)
		}
		end, ok := ParseClock(w.End)
		if !ok {
			return s, bad(f("end"), "Window %d: the end must be a time like 06:00.", n)
		}
		if start == end {
			return s, bad(f("end"), "Window %d: the end must differ from the start.", n)
		}
		if math.IsNaN(w.Rate) || math.IsInf(w.Rate, 0) || w.Rate < 0 || w.Rate > MaxRate {
			return s, bad(f("rate"), "Window %d: the rate must be between 0 and %g per kWh.", n, MaxRate)
		}
		label := strings.TrimSpace(w.Label)
		if utf8.RuneCountInString(label) > MaxLabelLen {
			return s, bad(f("label"), "Window %d: the label is longer than %d characters.", n, MaxLabelLen)
		}
		for _, c := range label {
			if unicode.IsControl(c) || c == '<' || c == '>' {
				return s, bad(f("label"), "Window %d: the label has characters that are not allowed.", n)
			}
		}
		for _, day := range daysOf(w.Days) {
			mark := func(d, from, to int) *FieldError {
				for m := from; m < to; m++ {
					if o := owner[d][m]; o != 0 {
						return bad(f("start"), "Window %d overlaps window %d.", n, int(o))
					}
					owner[d][m] = int8(i + 1)
				}
				return nil
			}
			if end > start {
				if e := mark(day, start, end); e != nil {
					return s, e
				}
			} else { // crosses midnight: the rest of this day, the start of the next
				if e := mark(day, start, minutesInDay); e != nil {
					return s, e
				}
				if e := mark((day+1)%7, 0, end); e != nil {
					return s, e
				}
			}
		}
		out.Windows[i] = Window{Days: w.Days, Start: w.Start, End: w.End, Rate: math.Round(w.Rate*1e4) / 1e4, Label: label}
	}
	return out, nil
}

// daysOf lists the weekdays (time.Weekday numbering, Sunday 0) of a window.
func daysOf(d string) []int {
	switch d {
	case Weekdays:
		return []int{1, 2, 3, 4, 5}
	case Weekends:
		return []int{6, 0}
	}
	return []int{0, 1, 2, 3, 4, 5, 6}
}

// ParseClock reads "HH:MM" (00:00 to 23:59) as minutes since midnight.
func ParseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || s[0] == '+' || s[0] == '-' || s[3] == '+' || s[3] == '-' {
		return 0, false
	}
	return h*60 + m, true
}

// Instance is one occurrence of a window on a particular night or day, as
// instants.
type Instance struct {
	Start, End time.Time
	Rate       float64
}

// Instances lists every occurrence of the schedule's windows that overlaps
// [from, to), in the schedule's own zone. Wall-clock times follow the zone's
// rules: on a spring-forward day a window minute that does not exist is skipped
// over by the clock, on a fall-back day the repeated hour is part of the window.
// The schedule must have passed Normalize.
func (s Schedule) Instances(from, to time.Time) []Instance {
	loc, err := time.LoadLocation(s.TZ)
	if err != nil || len(s.Windows) == 0 || !to.After(from) {
		return nil
	}
	fl, tl := from.In(loc), to.In(loc)
	day := time.Date(fl.Year(), fl.Month(), fl.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -1)
	last := time.Date(tl.Year(), tl.Month(), tl.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, 1)
	var out []Instance
	for ; !day.After(last); day = day.AddDate(0, 0, 1) {
		wd := int(day.Weekday())
		for _, w := range s.Windows {
			if !hasDay(w.Days, wd) {
				continue
			}
			sm, _ := ParseClock(w.Start)
			em, _ := ParseClock(w.End)
			y, m, d := day.Date()
			st := time.Date(y, m, d, sm/60, sm%60, 0, 0, loc)
			ed := time.Date(y, m, d, em/60, em%60, 0, 0, loc)
			if em <= sm {
				ed = time.Date(y, m, d+1, em/60, em%60, 0, 0, loc)
			}
			if ed.After(from) && st.Before(to) {
				out = append(out, Instance{Start: st, End: ed, Rate: w.Rate})
			}
		}
	}
	return out
}

func hasDay(days string, wd int) bool {
	for _, d := range daysOf(days) {
		if d == wd {
			return true
		}
	}
	return false
}
