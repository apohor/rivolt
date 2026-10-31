//go:build integration

package drives_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/testpg"
)

// ListRange returns the drives that started in [from, to) and, when the window holds more rows than the ceiling, fails closed
// instead of returning the newest few (a year's totals must never be short).
func TestListRange_WindowAndCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, _ := db.EnsureUser(ctx, pool, "driverange")
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	day := time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		d := sampleDrive("rng-"+strconv.Itoa(i), "rv-range", day.AddDate(0, 0, i))
		if err := store.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListRange(ctx, day, day.AddDate(0, 0, 4)) // the first four days; the edge is half-open
	if err != nil || len(rows) != 4 {
		t.Fatalf("window: %d rows, %v", len(rows), err)
	}
	defer drives.SetRangeLimitForTest(5)()
	if _, err := store.ListRange(ctx, day, day.AddDate(0, 0, 6)); !errors.Is(err, drives.ErrRangeTooLarge) {
		t.Fatalf("6 rows over a ceiling of 5: %v", err)
	}
	if rows, err := store.ListRange(ctx, day, day.AddDate(0, 0, 5)); err != nil || len(rows) != 5 {
		t.Fatalf("exactly the ceiling is allowed: %d, %v", len(rows), err)
	}
}
