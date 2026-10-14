package electrafi

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A7: sessions the live recorder already captured are not imported
// again (the recorder's row is authoritative).
func TestImportReader_SkipsSessionsCoveredByLive(t *testing.T) {
	csv := strings.Join([]string{
		"Date,battery_level,odometer,latitude,longitude,speed,shift_state,charging_state,driveNumber,chargeNumber",
		"2026-10-01 08:00:00,80,1000,30.27,-97.74,30,D,,7,0",
		"2026-10-01 08:10:00,78,1005,30.30,-97.70,40,D,,7,0",
		"2026-10-01 08:20:00,76,1010,30.33,-97.66,35,D,,7,0",
		"2026-10-01 18:00:00,40,1010,30.27,-97.74,0,P,Charging,0,3",
		"2026-10-01 19:00:00,50,1010,30.27,-97.74,0,P,Charging,0,3",
		"2026-10-01 20:00:00,60,1010,30.27,-97.74,0,P,Complete,0,3",
	}, "\n")
	var asked []string
	imp := &Importer{
		VehicleID: "rv-1",
		Location:  time.UTC,
		LiveCovers: func(_ context.Context, kind string, start, end time.Time) (bool, error) {
			asked = append(asked, kind+" "+start.Format("15:04")+"-"+end.Format("15:04"))
			return true, nil
		},
	}
	res, err := imp.ImportReader(context.Background(), "t.csv", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ImportReader: %v", err)
	}
	if res.Drives != 0 || res.Charges != 0 || res.SkippedLive != 2 {
		t.Errorf("result = %+v, want 0 drives, 0 charges, 2 skipped as live", res)
	}
	want := map[string]bool{"drive 08:00-08:20": true, "charge 18:00-20:00": true}
	if len(asked) != 2 || !want[asked[0]] || !want[asked[1]] {
		t.Errorf("live checks = %v, want %v", asked, want)
	}
}
