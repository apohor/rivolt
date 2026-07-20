package rivian

import (
	"testing"
	"time"
)

// frame builds a full-snapshot State at base+min with the given lifecycle
// fields — the shape a vehicle_state row exports to.
func frame(base time.Time, min int, gear string, odoKm, soc, kph float64) State {
	return State{
		At:              base.Add(time.Duration(min) * time.Minute),
		Gear:            gear,
		OdometerKm:      odoKm,
		BatteryLevelPct: soc,
		SpeedKph:        kph,
		Latitude:        37.0 + float64(min)*0.01,
		Longitude:       -122.0 - float64(min)*0.01,
	}
}

// A clean P → D…D → P sequence replays to exactly one drive, with the
// distance and SoC delta the frames imply.
func TestReplay_SingleCleanDrive(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	r := NewReplayer("veh-1")
	r.FeedAll([]State{
		frame(base, 0, "P", 1000, 80, 0),
		frame(base, 1, "D", 1000, 80, 50),
		frame(base, 2, "D", 1005, 79, 50),
		frame(base, 3, "D", 1010, 78, 50),
		frame(base, 4, "P", 1010, 78, 0), // speed 0 → close confirms
	})
	got := r.Drives()
	if len(got) != 1 {
		t.Fatalf("got %d drives, want 1", len(got))
	}
	d := got[0]
	// 10 km ≈ 6.2 mi.
	if d.DistanceMi < 5 || d.DistanceMi > 7 {
		t.Errorf("distance %.2f mi, want ~6.2", d.DistanceMi)
	}
	if d.StartSoCPct != 80 || d.EndSoCPct != 78 {
		t.Errorf("SoC %.0f->%.0f, want 80->78", d.StartSoCPct, d.EndSoCPct)
	}
}

// Two real drives separated by a park gap longer than the reopen window
// must stay two drives — the merge-on-reopen only folds near-instant
// D-P-D blips, not a genuine stop.
func TestReplay_TwoDrivesWithParkBetween(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	r := NewReplayer("veh-1")
	r.FeedAll([]State{
		frame(base, 0, "P", 1000, 80, 0),
		frame(base, 1, "D", 1000, 80, 50),
		frame(base, 2, "D", 1005, 79, 50),
		frame(base, 3, "P", 1005, 79, 0), // close drive 1
		frame(base, 8, "P", 1005, 79, 0), // parked; 5-min gap > reopen window
		frame(base, 9, "D", 1005, 79, 50), // opens drive 2
		frame(base, 10, "D", 1010, 78, 50),
		frame(base, 11, "P", 1010, 78, 0), // close drive 2
	})
	got := r.Drives()
	if len(got) != 2 {
		t.Fatalf("got %d drives, want 2 (park gap should not merge them)", len(got))
	}
}
