package leases

import (
	"testing"
	"time"
)

func TestFairShare(t *testing.T) {
	for _, c := range []struct{ g, n, want int }{{10, 1, 11}, {10, 2, 6}, {12, 3, 5}, {12, 5, 4}, {0, 3, 1}, {5, 0, 6}} {
		if got := fairShare(c.g, c.n); got != c.want {
			t.Errorf("fairShare(%d,%d) = %d, want %d", c.g, c.n, got, c.want)
		}
	}
}

func TestNewestGeneration(t *testing.T) {
	now := time.Now()
	pods := []livePod{
		{id: "o1", generation: "g1", startedAt: now.Add(-time.Hour)},
		{id: "o2", generation: "g1", startedAt: now.Add(-50 * time.Minute)},
		{id: "n1", generation: "g2", startedAt: now.Add(-time.Minute)},
		{id: "n2", generation: "g3", startedAt: now, draining: true}, // draining never counts
	}
	if got := newestGeneration(pods); got != "g2" {
		t.Errorf("newest = %q, want g2", got)
	}
	if got := newestGeneration(nil); got != "" {
		t.Errorf("newest of none = %q", got)
	}
}

// A rollout is a pod draining or two pod generations live: nothing is shed for the fair share meanwhile, so a group moves once.
func TestRollingUpdate(t *testing.T) {
	for name, tc := range map[string]struct {
		pods []livePod
		want bool
	}{
		"one pod":                 {[]livePod{{id: "a", generation: "g1"}}, false},
		"steady, same generation": {[]livePod{{id: "a", generation: "g1"}, {id: "b", generation: "g1"}}, false},
		"two generations":         {[]livePod{{id: "a", generation: "g1"}, {id: "b", generation: "g2"}}, true},
		"one draining":            {[]livePod{{id: "a", generation: "g1", draining: true}, {id: "b", generation: "g1"}}, true},
		"generation unset":        {[]livePod{{id: "a"}, {id: "b"}}, false},
		"one unset one set":       {[]livePod{{id: "a"}, {id: "b", generation: "g1"}}, false},
	} {
		if got := rollingUpdate(tc.pods); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
