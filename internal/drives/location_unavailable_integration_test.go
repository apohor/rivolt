//go:build integration

package drives_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/drives"
)

// A live drive's "the truck sent no location" flag: set and cleared by the live recorder's rows, carried by the list and by Get, left
// alone by any other source.
func TestLocationUnavailableFlag(t *testing.T) {
	factory, uidA, _ := setupEnv(t)
	a := factory.For(uidA)
	ctx := context.Background()
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	get := func(id string) bool {
		t.Helper()
		d, err := a.Get(ctx, id)
		if err != nil || d == nil {
			t.Fatalf("Get %s: %v %v", id, d, err)
		}
		return d.LocationUnavailable
	}

	d := sampleDrive("loc-1", "rv-loc-A", start)
	d.Source = "live"
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if get("loc-1") {
		t.Fatal("a new drive is flagged")
	}
	d.LocationUnavailable = true
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if !get("loc-1") {
		t.Fatal("the live row did not set the flag")
	}
	// The list carries it too.
	rows, err := a.ListRecentForVehicle(ctx, "rv-loc-A", 10, false)
	if err != nil || len(rows) == 0 || !rows[0].LocationUnavailable {
		t.Fatalf("the list lost the flag: %v %v", rows, err)
	}
	// Another source (an import of the same id) leaves it alone.
	imp := d
	imp.Source = "electrafi"
	imp.LocationUnavailable = false
	if err := a.Upsert(ctx, imp); err != nil {
		t.Fatal(err)
	}
	if !get("loc-1") {
		t.Error("an import cleared the live recorder's flag")
	}
	// The live recorder later gets fixes (a resumed drive): it clears the flag.
	d.LocationUnavailable = false
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if get("loc-1") {
		t.Error("the live row did not clear the flag")
	}
	// A second flagged live drive round-trips through EachAll, the backup iterator.
	d2 := sampleDrive("loc-2", "rv-loc-A", start.Add(time.Hour))
	d2.Source, d2.LocationUnavailable = "live", true
	if err := a.Upsert(ctx, d2); err != nil {
		t.Fatal(err)
	}
	seen := false
	if err := a.EachAll(ctx, func(x drives.Drive) error {
		if x.ID == "loc-2" {
			seen = x.LocationUnavailable
		}
		return nil
	}); err != nil || !seen {
		t.Errorf("EachAll lost the flag: %v %v", seen, err)
	}
}
