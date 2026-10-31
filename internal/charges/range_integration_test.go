//go:build integration

package charges_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// ListRange returns the charges that started in [from, to) and fails closed over the row ceiling.
func TestListRange_WindowAndCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, _ := db.EnsureUser(ctx, pool, "chargerange")
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	day := time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		if err := store.Upsert(ctx, sampleCharge("rng-"+strconv.Itoa(i), "rv-range", day.AddDate(0, 0, i), "charging_complete")); err != nil {
			t.Fatal(err)
		}
	}
	if rows, err := store.ListRange(ctx, day, day.AddDate(0, 0, 4)); err != nil || len(rows) != 4 {
		t.Fatalf("window: %d rows, %v", len(rows), err)
	}
	defer charges.SetRangeLimitForTest(5)()
	if _, err := store.ListRange(ctx, day, day.AddDate(0, 0, 6)); !errors.Is(err, charges.ErrRangeTooLarge) {
		t.Fatalf("6 rows over a ceiling of 5: %v", err)
	}
}
