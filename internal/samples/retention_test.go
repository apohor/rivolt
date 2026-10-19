package samples

import (
	"testing"
	"time"
)

func TestRetentionCutoff(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	for _, c := range []struct {
		now    string
		months int
		want   string
	}{
		{"2027-11-05 10:00", 12, "2026-11-01 00:00"}, // October 2026 and older go
		{"2027-11-01 00:00", 12, "2026-11-01 00:00"},
		{"2027-10-31 23:59", 12, "2026-10-01 00:00"},
		{"2028-03-31 12:00", 1, "2028-02-01 00:00"}, // no "Feb 31" overflow
		{"2028-02-29 12:00", 12, "2027-02-01 00:00"},
		{"2027-01-01 00:00", 3, "2026-10-01 00:00"},
		{"2027-11-05 10:00", 0, "2027-11-01 00:00"}, // the current month
	} {
		if got := RetentionCutoff(d(c.now), c.months); !got.Equal(d(c.want)) {
			t.Errorf("RetentionCutoff(%s, %d) = %s, want %s", c.now, c.months, got.Format(time.DateTime), c.want)
		}
	}
	// A zone east of UTC: the month is UTC's, like the partition names.
	tokyo := time.FixedZone("JST", 9*3600)
	if got := RetentionCutoff(time.Date(2027, 11, 1, 5, 0, 0, 0, tokyo), 12); !got.Equal(d("2026-10-01 00:00")) {
		t.Errorf("Tokyo Nov 1 05:00 is Oct 31 UTC: got %s", got)
	}
}

func TestParseRetentionMonths(t *testing.T) {
	for in, want := range map[string]int{"": 0, "0": 0, " 12 ": 12, "3": 3, "36": 36} {
		if got, err := ParseRetentionMonths(in); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"1", "2", "-1", "12m", "1.5"} {
		if _, err := ParseRetentionMonths(in); err == nil {
			t.Errorf("%q: accepted", in)
		}
	}
	if NewRetention(nil, 12) != nil || NewRetention(nil, 0) != nil {
		t.Error("no db or 0 months must be no sweep")
	}
}
