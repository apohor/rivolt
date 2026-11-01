package entitlements

import "testing"

// A read that finds no rows still reports every known (source, status) pair, as 0, and keeps the counts it did find.
func TestFillKnownReportsEveryPair(t *testing.T) {
	m := map[[2]string]int{}
	fillKnown(m)
	if want := len(knownSources) * len(knownStatuses); len(m) != want {
		t.Fatalf("%d pairs for an empty table, want %d", len(m), want)
	}
	if v, ok := m[[2]string{SourceFounding, StatusActive}]; !ok || v != 0 {
		t.Errorf("founding/active = %d (present %v), want a 0 series", v, ok)
	}
	m = map[[2]string]int{{SourceStripe, StatusPastDue}: 4}
	fillKnown(m)
	if m[[2]string{SourceStripe, StatusPastDue}] != 4 || m[[2]string{SourceStripe, StatusActive}] != 0 {
		t.Errorf("fillKnown changed or dropped a count: %v", m)
	}
}
