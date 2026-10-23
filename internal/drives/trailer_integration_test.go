//go:build integration

package drives_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/drives"
)

func pb(v bool) *bool { return &v }

// The trailer flag and the user's correction: what the recorder saw, what the user said, and how each is kept.
func TestTrailerAndItsOverride(t *testing.T) {
	factory, uidA, uidB := setupEnv(t)
	a, b := factory.For(uidA), factory.For(uidB)
	ctx := context.Background()
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	flag := func(v *bool) string {
		if v == nil {
			return "nil"
		}
		if *v {
			return "true"
		}
		return "false"
	}
	state := func() (string, string, string) {
		t.Helper()
		d, err := a.Get(ctx, "tr-1")
		if err != nil || d == nil {
			t.Fatalf("Get: %v %v", d, err)
		}
		return flag(d.Trailer), flag(d.TrailerOverride), flag(d.HasTrailer())
	}

	// A drive recorded without the trailer state: all NULL.
	d := sampleDrive("tr-1", "rv-trailer-A", start)
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if det, over, eff := state(); det != "nil" || over != "nil" || eff != "nil" {
		t.Fatalf("a new drive with no trailer reading: %s %s %s", det, over, eff)
	}
	// The recorder then saw a trailer; a later recording that never knew it (nil) leaves it.
	d.Trailer = pb(true)
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Trailer = nil
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if det, _, eff := state(); det != "true" || eff != "true" {
		t.Fatalf("a re-recording that never knew the trailer changed it: %s %s", det, eff)
	}
	// The user says it was not a trailer (a bike rack with lights): effective false, recorded still true.
	if ok, err := a.SetTrailerOverride(ctx, "tr-1", pb(false)); !ok || err != nil {
		t.Fatalf("SetTrailerOverride: %v %v", ok, err)
	}
	if det, over, eff := state(); det != "true" || over != "false" || eff != "false" {
		t.Fatalf("after the user's correction: %s %s %s", det, over, eff)
	}
	// A later recording (the live drive growing, a repair, a re-import) never clears the correction, whatever it saw.
	d.Trailer = pb(true)
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Trailer = pb(false)
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if det, over, eff := state(); det != "false" || over != "false" || eff != "false" {
		t.Fatalf("a re-recording touched the correction: %s %s %s", det, over, eff)
	}
	// A restore carries a correction (a non-nil value is set).
	d.TrailerOverride = pb(true)
	if err := a.Upsert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, over, eff := state(); over != "true" || eff != "true" {
		t.Fatalf("a restored correction: %s %s", over, eff)
	}
	// The listings and the backup iterator carry both columns.
	rows, err := a.ListRecentForVehicle(ctx, "rv-trailer-A", 10, false)
	if err != nil || len(rows) != 1 || flag(rows[0].Trailer) != "false" || flag(rows[0].TrailerOverride) != "true" {
		t.Fatalf("ListRecentForVehicle: %+v %v", rows, err)
	}
	var seen int
	if err := a.EachAll(ctx, func(x drives.Drive) error {
		seen++
		if flag(x.Trailer) != "false" || flag(x.TrailerOverride) != "true" {
			t.Errorf("EachAll: %s %s", flag(x.Trailer), flag(x.TrailerOverride))
		}
		return nil
	}); err != nil || seen != 1 {
		t.Fatalf("EachAll: %d %v", seen, err)
	}
	// Clearing it hands the drive back to the recording.
	if ok, err := a.SetTrailerOverride(ctx, "tr-1", nil); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if det, over, eff := state(); det != "false" || over != "nil" || eff != "false" {
		t.Fatalf("after clearing: %s %s %s", det, over, eff)
	}
	// Another user's drive id, or none: not found, nothing changed.
	if ok, _ := b.SetTrailerOverride(ctx, "tr-1", pb(true)); ok {
		t.Error("another user set the correction on this user's drive")
	}
	if ok, _ := a.SetTrailerOverride(ctx, "no-such-drive", pb(true)); ok {
		t.Error("a correction on a drive that does not exist reported success")
	}
	if _, over, _ := state(); over != "nil" {
		t.Errorf("a refused write changed the correction: %s", over)
	}
}
