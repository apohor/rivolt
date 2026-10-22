//go:build integration

package charges_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

// #906: a place the user set ("set by you") survives every re-classification: a
// home-location correction, its undo and a re-import of the same charge. Clearing it
// gives the place back to the home location. The location override is a flag on the
// row. Both are per user, and a restore can't store a value outside the two lists.
func TestChargeOverridesSurviveReclassification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "override-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "override-b")
	if err != nil {
		t.Fatal(err)
	}
	f := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	store := f.For(uid)
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	// Synthetic positions: the charge and a home 10 km apart.
	const lat, lon = 30.27, -97.74
	const farLat = 30.36
	pinned := sampleCharge("ov-pinned", "ov-rv", base, "charging_complete")
	pinned.Place = "away"
	free := sampleCharge("ov-free", "ov-rv", base.Add(24*time.Hour), "charging_complete")
	free.Place = "away"
	for _, c := range []charges.Charge{pinned, free} {
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) *charges.Charge {
		t.Helper()
		c, err := store.GetByExternalID(ctx, id)
		if err != nil || c == nil {
			t.Fatalf("get %s: %v %v", id, c, err)
		}
		return c
	}

	// The user says the far charge was at home.
	if err := store.SetPlaceOverride(ctx, "ov-pinned", "home", true, farLat, lon, 250); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "home" || c.PlaceOverride != "home" {
		t.Fatalf("after set: place %q override %q", c.Place, c.PlaceOverride)
	}

	// A correction to a home at the charge: the free row moves, the pinned one is skipped.
	res, err := store.RestampPlace(ctx, charges.RestampOptions{HomeLat: lat, HomeLon: lon, RadiusM: 250,
		IsDCFC: func(float64, float64, float64, time.Duration) bool { return false }})
	if err != nil || res.RestampID == nil {
		t.Fatalf("restamp: %+v %v", res, err)
	}
	if res.SkippedOverridden != 1 {
		t.Errorf("skipped_overridden = %d, want 1", res.SkippedOverridden)
	}
	// And a correction away from it.
	res2, err := store.RestampPlace(ctx, charges.RestampOptions{HomeLat: farLat, HomeLon: lon, RadiusM: 250,
		IsDCFC: func(float64, float64, float64, time.Duration) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "home" {
		t.Errorf("after corrections: %q, want the user's home", c.Place)
	}
	if res2.RestampID != nil {
		if _, err := store.UndoRestamp(ctx, res2.RestampID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UndoRestamp(ctx, res.RestampID); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "home" || c.PlaceOverride != "home" {
		t.Errorf("after undo: place %q override %q", c.Place, c.PlaceOverride)
	}

	// A re-import of the same session carries the recorder's place and no override.
	if err := store.Upsert(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "home" || c.PlaceOverride != "home" {
		t.Errorf("after re-import: place %q override %q", c.Place, c.PlaceOverride)
	}

	// Another user can't reach the row.
	if err := f.For(other).SetPlaceOverride(ctx, "ov-pinned", "away", false, 0, 0, 250); !errors.Is(err, charges.ErrNoSuchCharge) {
		t.Errorf("other user = %v, want ErrNoSuchCharge", err)
	}
	if err := f.For(other).SetLocationOverride(ctx, "ov-pinned", true); !errors.Is(err, charges.ErrNoSuchCharge) {
		t.Errorf("other user location = %v, want ErrNoSuchCharge", err)
	}
	if err := store.SetPlaceOverride(ctx, "ov-pinned", "garage", true, lat, lon, 250); err == nil {
		t.Error("an unknown place was accepted")
	}

	// Clearing hands it back to the home location: 10 km from home is away, at home is home.
	if err := store.SetPlaceOverride(ctx, "ov-pinned", "", true, farLat, lon, 250); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "away" || c.PlaceOverride != "" {
		t.Errorf("cleared, far home: place %q override %q", c.Place, c.PlaceOverride)
	}
	if err := store.SetPlaceOverride(ctx, "ov-pinned", "", true, lat, lon, 250); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "home" {
		t.Errorf("cleared, near home: place %q", c.Place)
	}
	if err := store.SetPlaceOverride(ctx, "ov-pinned", "", false, 0, 0, 250); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-pinned"); c.Place != "" {
		t.Errorf("cleared, no home: place %q, want none", c.Place)
	}
	if err := store.SetPlaceOverride(ctx, "ov-missing", "", true, lat, lon, 250); !errors.Is(err, charges.ErrNoSuchCharge) {
		t.Errorf("missing id = %v, want ErrNoSuchCharge", err)
	}

	// Location: set, kept by a re-import, cleared.
	if err := store.SetLocationOverride(ctx, "ov-free", true); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, free); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-free"); c.LocationOverride != charges.LocationRecorded {
		t.Errorf("location after re-import: %q", c.LocationOverride)
	}
	if err := store.SetLocationOverride(ctx, "ov-free", false); err != nil {
		t.Fatal(err)
	}
	if c := get("ov-free"); c.LocationOverride != "" {
		t.Errorf("location after clear: %q", c.LocationOverride)
	}

	// A restore carries the overrides; anything else is stored as none.
	restored := sampleCharge("ov-restored", "ov-rv", base.Add(48*time.Hour), "charging_complete")
	restored.Place, restored.PlaceOverride, restored.LocationOverride = "home", "away", charges.LocationRecorded
	odd := sampleCharge("ov-odd", "ov-rv", base.Add(50*time.Hour), "charging_complete")
	odd.PlaceOverride, odd.LocationOverride = "garage", "snapped"
	for _, c := range []charges.Charge{restored, odd} {
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if c := get("ov-restored"); c.Place != "away" || c.PlaceOverride != "away" || c.LocationOverride != charges.LocationRecorded {
		t.Errorf("restored: place %q override %q location %q", c.Place, c.PlaceOverride, c.LocationOverride)
	}
	if c := get("ov-odd"); c.PlaceOverride != "" || c.LocationOverride != "" {
		t.Errorf("odd values stored: %q %q", c.PlaceOverride, c.LocationOverride)
	}
	all, err := store.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, c := range all {
		if c.ID == "ov-restored" {
			seen = c.PlaceOverride == "away" && c.LocationOverride == charges.LocationRecorded
		}
	}
	if !seen {
		t.Error("ListAll (export, backup) lost the overrides")
	}
}
