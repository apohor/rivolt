package electrafi

import (
	"context"
	"strings"
	"testing"
	"time"
)

// With a sample retention cutoff, raw samples older than it are left out and
// counted, while the drive and charge derived from the same rows are still
// processed (the importer would otherwise recreate a dropped partition for the
// next retention sweep to delete).
func TestImportReader_SkipsSamplesPastRetentionKeepsDrivesAndCharges(t *testing.T) {
	csv := strings.Join([]string{
		"Date,battery_level,odometer,latitude,longitude,speed,shift_state,charging_state,driveNumber,chargeNumber",
		"2024-03-01 08:00:00,80,1000,30.27,-97.74,30,D,,7,0",
		"2024-03-01 08:10:00,78,1005,30.30,-97.70,40,D,,7,0",
		"2024-03-01 08:20:00,76,1010,30.33,-97.66,35,D,,7,0",
		"2024-03-01 18:00:00,40,1010,30.27,-97.74,0,P,Charging,0,3",
		"2024-03-01 19:00:00,50,1010,30.27,-97.74,0,P,Charging,0,3",
		"2024-03-01 20:00:00,60,1010,30.27,-97.74,0,P,Complete,0,3",
		"2026-10-01 08:00:00,60,1010,30.27,-97.74,0,P,,0,0",
		"2026-10-01 08:05:00,60,1010,30.27,-97.74,0,P,,0,0",
	}, "\n")
	// Live-covered, so no store is written; the derived drive and charge show up
	// as SkippedLive, which proves they were still derived from the old rows.
	covered := func(context.Context, string, time.Time, time.Time) (bool, error) { return true, nil }
	run := func(from time.Time) Result {
		imp := &Importer{VehicleID: "rv-1", Location: time.UTC, SampleFrom: from, LiveCovers: covered}
		res, err := imp.ImportReader(context.Background(), "t.csv", strings.NewReader(csv))
		if err != nil {
			t.Fatalf("ImportReader: %v", err)
		}
		return res
	}

	all := run(time.Time{})
	if all.SamplesPastRetention != 0 || all.SkippedLive != 2 {
		t.Fatalf("no cutoff: %+v, want no samples skipped and the drive and charge derived", all)
	}
	cut := run(time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC))
	if cut.SamplesPastRetention != 6 {
		t.Errorf("past retention = %d, want the 6 rows of 2024", cut.SamplesPastRetention)
	}
	if cut.SkippedLive != 2 {
		t.Errorf("with a cutoff: %d sessions derived, want the old drive and charge still processed", cut.SkippedLive)
	}
}
