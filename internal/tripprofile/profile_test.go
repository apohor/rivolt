package tripprofile

import (
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/db"
)

func TestMultiplier_FactoryBaseline(t *testing.T) {
	// Stock R1: 21" wheels, all-season tires, no accessories, no
	// load. The whole point of the factory baseline is that it has
	// no effect on the plan - guards against an accidental
	// non-1.0 default value.
	if got := Multiplier(db.VehicleProfile{}); got != 1.0 {
		t.Errorf("zero profile: got %v want 1.0", got)
	}
	if got := Multiplier(db.VehicleProfile{WheelInches: 21, TireType: "all_season"}); got != 1.0 {
		t.Errorf("explicit 21 + all-season: got %v want 1.0", got)
	}
}

func TestMultiplier_TwentyTwoWheelsATTires(t *testing.T) {
	// The most-common "I have a bigger setup" combo. 1.04 * 1.06 =
	// 1.1024.
	got := Multiplier(db.VehicleProfile{WheelInches: 22, TireType: "all_terrain"})
	want := 1.1024
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("22\" + AT: got %v want ~%v", got, want)
	}
}

func TestMultiplier_RooftopTentDominates(t *testing.T) {
	// Rooftop tent alone (1.12) should be the contribution; adding
	// roof_rack on top should NOT double-count - the tent already
	// implies a rack.
	tent := Multiplier(db.VehicleProfile{Accessories: []string{"rooftop_tent"}})
	tentRack := Multiplier(db.VehicleProfile{Accessories: []string{"rooftop_tent", "roof_rack"}})
	if math.Abs(tent-tentRack) > 1e-6 {
		t.Errorf("rooftop_tent should swallow roof_rack: tent=%v tent+rack=%v", tent, tentRack)
	}
	if math.Abs(tent-1.12) > 1e-6 {
		t.Errorf("rooftop_tent: got %v want 1.12", tent)
	}
}

func TestMultiplier_CargoBoxPlusBikeRack(t *testing.T) {
	// User who hauls bikes + a cargo box: 1.06 * 1.02 = 1.0812.
	got := Multiplier(db.VehicleProfile{Accessories: []string{"cargo_box", "bike_rack"}})
	want := 1.06 * 1.02
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("cargo + bike: got %v want %v", got, want)
	}
}

func TestMultiplier_LoadCappedAt500lb(t *testing.T) {
	// 500 lb = 2.5%, well under the 5% cap.
	if got := Multiplier(db.VehicleProfile{DefaultExtraLoadLb: 500}); math.Abs(got-1.025) > 1e-6 {
		t.Errorf("500lb: got %v want 1.025", got)
	}
	// 2000 lb hits the per-factor cap at 1.05 - the rest is absorbed.
	if got := Multiplier(db.VehicleProfile{DefaultExtraLoadLb: 2000}); math.Abs(got-1.05) > 1e-6 {
		t.Errorf("2000lb (cap): got %v want 1.05", got)
	}
}

func TestMultiplier_GlobalCap(t *testing.T) {
	// Worst stacked: 22" + AT + rooftop_tent + 500 lb. Product
	// crosses the global 1.5 ceiling? Compute: 1.04 * 1.06 * 1.12 *
	// 1.025 = 1.265. Doesn't hit the cap, which is the point - the
	// cap is only there for fantasy combos.
	got := Multiplier(db.VehicleProfile{
		WheelInches: 22, TireType: "all_terrain",
		Accessories:        []string{"rooftop_tent"},
		DefaultExtraLoadLb: 500,
	})
	if got > 1.5 || got < 1.25 {
		t.Errorf("worst common case: got %v want ~1.27", got)
	}
}

func TestMultiplier_Summer18Floor(t *testing.T) {
	// Best-case efficiency play: 18" + summer + no load. 0.97 *
	// 0.99 = 0.9603, well above the 0.85 floor.
	got := Multiplier(db.VehicleProfile{WheelInches: 18, TireType: "summer"})
	if math.Abs(got-0.97*0.99) > 1e-3 {
		t.Errorf("18\" + summer: got %v want 0.9603", got)
	}
}

func TestReasons_OnlyContributorsListed(t *testing.T) {
	// Baseline profile: no reasons.
	if rr := Reasons(db.VehicleProfile{}); len(rr) != 0 {
		t.Errorf("baseline reasons: got %v want []", rr)
	}
	// Three contributors: 22" + AT + 200 lb. Accessories empty so
	// not in the list.
	rr := Reasons(db.VehicleProfile{
		WheelInches: 22, TireType: "all_terrain",
		DefaultExtraLoadLb: 200,
	})
	if len(rr) != 3 {
		t.Fatalf("3-contributor profile: got %d reasons (%v)", len(rr), rr)
	}
	labels := map[string]bool{}
	for _, r := range rr {
		labels[r.Label] = true
	}
	if !labels["22-inch wheels"] || !labels["AT tires"] {
		t.Errorf("expected wheel + tire labels in %v", rr)
	}
}

func TestReasons_RoofRackAlone(t *testing.T) {
	rr := Reasons(db.VehicleProfile{Accessories: []string{"roof_rack"}})
	if len(rr) != 1 || rr[0].Label != "roof rack" {
		t.Errorf("got %v want single roof rack reason", rr)
	}
}
