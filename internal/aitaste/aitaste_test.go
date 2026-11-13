package aitaste

import (
	"testing"
	"time"
)

func TestMonthAndDayBoundariesAreUTC(t *testing.T) {
	// 23:30 in New York on Nov 30 is already December in UTC.
	ny := time.FixedZone("EST", -5*3600)
	now := time.Date(2026, 11, 30, 23, 30, 0, 0, ny)
	if got, want := MonthStart(now), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("MonthStart = %v, want %v", got, want)
	}
	if got, want := ResetAt(now), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", got, want)
	}
	if got, want := NextDay(now), time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextDay = %v, want %v", got, want)
	}
}

func TestMicroUSDRoundsToTheNearestMicro(t *testing.T) {
	if got := microUSD(0.0600004); got != 60000 {
		t.Errorf("microUSD = %d", got)
	}
}
