//go:build integration

package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/samples"
)

// The same import with and without a retention cutoff: with it the old rows are
// not in vehicle_state and are counted, the old drive is still imported; without
// it they all land. (Importing past the cutoff would recreate a partition the
// retention sweep dropped, for the next sweep to delete.)
func TestElectrafiImportHonoursSampleRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "imp-ret", "imp-ret@x.test", "imp-ret", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-imp-ret')`, uid); err != nil {
		t.Fatal(err)
	}
	deps := backupTestDepsFor(t, pool)
	ds, ss := deps.Drives.For(uid), deps.Samples.For(uid)

	now := time.Now().UTC()
	recent := now.Add(-48 * time.Hour).Truncate(time.Minute)
	old := time.Date(now.Year()-3, 3, 1, 8, 0, 0, 0, time.UTC)
	row := func(t0 time.Time, i int, shift string, drive int) string {
		return fmt.Sprintf("%s,%d,%d,30.27,-97.74,%d,%s,,%d,0", t0.Add(time.Duration(i)*10*time.Minute).Format("2006-01-02 15:04:05"), 80-i, 1000+5*i, 30*b2i(shift == "D"), shift, drive)
	}
	lines := []string{"Date,battery_level,odometer,latitude,longitude,speed,shift_state,charging_state,driveNumber,chargeNumber"}
	for i := 0; i < 3; i++ {
		lines = append(lines, row(old, i, "D", 7)) // an old drive: 3 rows
	}
	for i := 0; i < 2; i++ {
		lines = append(lines, row(recent, i, "P", 0)) // two recent parked rows
	}
	csv := strings.Join(lines, "\n")

	run := func(from time.Time) electrafi.Result {
		imp := &electrafi.Importer{Drives: ds, Samples: ss, VehicleID: "rv-imp-ret", Location: time.UTC, SampleFrom: from}
		res, err := imp.ImportReader(ctx, "t.csv", strings.NewReader(csv))
		if err != nil {
			t.Fatalf("ImportReader: %v", err)
		}
		return res
	}
	count := func() int {
		n, err := ss.Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	res := run(samples.RetentionCutoff(now, 24))
	if res.SamplesPastRetention != 3 || res.Samples != 2 || count() != 2 {
		t.Fatalf("with a cutoff: %+v, stored samples %d; want 3 past retention, 2 stored", res, count())
	}
	if res.Drives != 1 {
		t.Errorf("with a cutoff: %d drives, want the old drive still imported", res.Drives)
	}

	res = run(time.Time{})
	if res.SamplesPastRetention != 0 || res.Samples != 5 || count() != 5 {
		t.Fatalf("without a cutoff: %+v, stored samples %d; want all 5", res, count())
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
