package main

import "testing"

// Replay never shares live state under prod's keyspace: an unset prefix
// (which defaults to "rivolt") or "rivolt" itself is refused.
func TestReplayRedisPrefixOK(t *testing.T) {
	for prefix, want := range map[string]bool{
		"":               false,
		"   ":            false,
		"rivolt":         false,
		" rivolt ":       false,
		"rivolt-preview": true,
		"demo":           true,
	} {
		if got := replayRedisPrefixOK(prefix); got != want {
			t.Errorf("replayRedisPrefixOK(%q) = %v, want %v", prefix, got, want)
		}
	}
}
