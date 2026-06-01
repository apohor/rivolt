package api

import (
	"testing"
	"time"
)

func TestSecondsUntilUTCMidnight(t *testing.T) {
	cases := []struct {
		now  time.Time
		want int
	}{
		{time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 86400},
		{time.Date(2026, 6, 1, 23, 59, 59, 0, time.UTC), 1},
		{time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), 43200},
	}
	for _, c := range cases {
		if got := secondsUntilUTCMidnight(c.now); got != c.want {
			t.Errorf("secondsUntilUTCMidnight(%s) = %d, want %d", c.now, got, c.want)
		}
	}
	// Never returns < 1, even at the exact boundary.
	if got := secondsUntilUTCMidnight(time.Date(2026, 6, 1, 23, 59, 59, 999_000_000, time.UTC)); got < 1 {
		t.Errorf("got %d, want >= 1", got)
	}
}
