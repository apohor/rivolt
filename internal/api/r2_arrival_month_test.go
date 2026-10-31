package api

import (
	"testing"
	"time"
)

func TestParseR2ArrivalMonth(t *testing.T) {
	now := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"2026-10": "2026-10", "2026-12": "2026-12", " 2027-01 ": "2027-01", "2028-10": "2028-10",
		"2026-09": "", "2028-11": "", "2026-12-15": "", "December": "", "": "", "2026-13": "",
	} {
		got, ok := parseR2ArrivalMonth(in, now)
		if (want != "") != ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
}
