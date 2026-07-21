package rivian

import (
	"os"
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

// Replaying a real DB-exported drive (testdata/clean_drive.jsonl — a
// ~3.8 mi trip with parked flanks, captured on the clean single-owner
// feed) round-trips through the recorder to a small, sane set of drives
// with the right total distance. Proves the DB-export → replay pipeline
// works on real data, and anchors the fixture as a regression baseline.
func TestReplay_RealCleanDriveFixture(t *testing.T) {
	f, err := os.Open("testdata/clean_drive.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	frames, err := FramesFromJSONL(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 50 {
		t.Fatalf("fixture too small: %d frames", len(frames))
	}
	r := NewReplayer("veh-1")
	r.FeedAll(frames)
	got := r.Drives()
	// One real trip → 1 drive (2-3 if a mid-drive stop split it); assert
	// the aggregate so the fixture is a stable anchor, not brittle, and
	// so a phantom-storm (many stub drives) fails loudly.
	if len(got) < 1 || len(got) > 3 {
		t.Fatalf("got %d drives, want 1-3 for one real trip", len(got))
	}
	var total float64
	for _, d := range got {
		total += d.DistanceMi
	}
	if total < 3 || total > 5 {
		t.Errorf("total distance %.1f mi, want ~3.8", total)
	}
}

// A drive whose P (park) frame is never delivered — the car parks, the
// recorder misses the transition, and the gap stays under the 30-min
// stale guard — straddles the parking period into ONE oversized drive.
// This is the phantom-drive mechanism (the 150 mi non-drive we hit). It
// documents the CURRENT behavior and is the regression target for the
// no-movement / power_state close hardening: once that lands, flip the
// want to 2 drives.
func TestReplay_MissedParkFrameProducesPhantom(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	r := NewReplayer("veh-1")
	r.FeedAll([]State{
		frame(base, 0, "P", 1000, 80, 0),
		frame(base, 1, "D", 1000, 80, 50),
		frame(base, 3, "D", 1010, 79, 50), // trip 1 (~6 mi)
		// car parks here, but NO P frame arrives; 10-min gap < 30-min stale guard
		frame(base, 13, "D", 1010, 79, 50), // "trip 2" starts; trip 1 never closed
		frame(base, 15, "D", 1025, 78, 50), // trip 2 (~9 mi)
		frame(base, 17, "P", 1025, 78, 0),  // finally a P → closes the ONE straddling drive
	})
	got := r.Drives()
	if len(got) != 1 {
		t.Fatalf("got %d drives; the missed-park straddle currently yields 1 phantom", len(got))
	}
	// It swallowed both trips: ~25 km ≈ 15.5 mi, not ~6+9 as two drives.
	if got[0].DistanceMi < 12 {
		t.Errorf("phantom distance %.1f mi, expected it to swallow both trips (~15.5)", got[0].DistanceMi)
	}
}
