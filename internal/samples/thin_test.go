package samples

import (
	"testing"
	"time"
)

func TestParseThinAfterDays(t *testing.T) {
	for in, want := range map[string]int{"": 0, "0": 0, "90": 90, " 30 ": 30, "365": 365} {
		if got, err := ParseThinAfterDays(in); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"1", "29", "-1", "90d"} {
		if _, err := ParseThinAfterDays(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if NewThinner(nil, 90) != nil || NewThinner(nil, 0) != nil {
		t.Error("no db or 0 days must be no job")
	}
}

func TestThinCandidate(t *testing.T) {
	m := func(y, mo int) time.Time { return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC) }
	months := []time.Time{m(2026, 5), m(2026, 6), m(2026, 7), m(2026, 8), m(2026, 9), m(2026, 10)}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) // 90 days back: 2026-07-21
	got, ok := ThinCandidate(months, nil, now, 90)
	if !ok || !got.Equal(m(2026, 5)) {
		t.Fatalf("first candidate %v %v, want 2026-05", got, ok)
	}
	got, ok = ThinCandidate(months, map[time.Time]bool{m(2026, 5): true, m(2026, 6): true}, now, 90)
	if ok {
		t.Errorf("July ends 2026-08-01, after the 90-day limit 2026-07-21: got %v", got)
	}
	// On 2026-10-30 the 90-day limit is 2026-08-01: July is wholly older.
	got, ok = ThinCandidate(months, map[time.Time]bool{m(2026, 5): true, m(2026, 6): true}, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), 90)
	if !ok || !got.Equal(m(2026, 7)) {
		t.Errorf("Oct 30: %v %v, want 2026-07", got, ok)
	}
	// Every month a real run would reach, in its order (the dry run reports
	// them all).
	all := ThinCandidates(months, map[time.Time]bool{m(2026, 6): true}, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), 90)
	if len(all) != 2 || !all[0].Equal(m(2026, 5)) || !all[1].Equal(m(2026, 7)) {
		t.Errorf("candidates %v, want 2026-05, 2026-07", all)
	}
	if c := ThinCandidates(months, nil, now, 90); len(c) != 2 {
		t.Errorf("Oct 19: %v, want 2026-05 and 2026-06", c)
	}
}

func TestPartitionMonth(t *testing.T) {
	if m, ok := partitionMonth("vehicle_state_y2026_05"); !ok || !m.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("parse: %v %v", m, ok)
	}
	for _, bad := range []string{"vehicle_state_y2026_05_full", "vehicle_state_y2026_13", "vehicle_state_y2026_05_thin", "vehicle_state"} {
		if _, ok := partitionMonth(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if monthPartition(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) != "vehicle_state_y2026_05" {
		t.Error("monthPartition")
	}
}
