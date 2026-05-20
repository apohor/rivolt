package packhealth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeriveAcceptsClean(t *testing.T) {
	vid := uuid.New()
	cid := uuid.New()
	now := time.Now()
	sample, ok := Derive(ChargeInput{
		VehicleID:      vid,
		ChargeID:       cid,
		EndedAt:        now,
		StartSoCPct:    20,
		EndSoCPct:      80,
		EnergyAddedKWh: 60,
	})
	if !ok {
		t.Fatal("want ok for 60% delta + 60 kWh")
	}
	if got, want := sample.PackKWhEffective, 100.0; got != want {
		t.Errorf("pack: got %.2f want %.2f", got, want)
	}
	if sample.VehicleID != vid || sample.ChargeID != cid {
		t.Error("identity not preserved")
	}
}

func TestDeriveRejectsSmallDelta(t *testing.T) {
	_, ok := Derive(ChargeInput{
		StartSoCPct:    60,
		EndSoCPct:      75, // 15% — under MinSoCDeltaPct
		EnergyAddedKWh: 15,
	})
	if ok {
		t.Fatal("want reject for SoC delta < MinSoCDeltaPct")
	}
}

func TestDeriveRejectsMissingEnergy(t *testing.T) {
	_, ok := Derive(ChargeInput{
		StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 0,
	})
	if ok {
		t.Fatal("want reject for missing energy")
	}
}

func TestDeriveRejectsCorruptHighEnergy(t *testing.T) {
	_, ok := Derive(ChargeInput{
		StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 250,
	})
	if ok {
		t.Fatal("want reject for energy > SanityMaxEnergyKWh")
	}
}

func TestDeriveRejectsImpossibleFit(t *testing.T) {
	// Tiny delta + sane energy → huge pack estimate. Should bail
	// at the post-fit 250 kWh cap even if delta passed.
	// We pick 31% delta (just above MinSoCDelta) and 100 kWh:
	//   pack = 100 / 0.31 = ~322 → over cap.
	_, ok := Derive(ChargeInput{
		StartSoCPct: 0, EndSoCPct: 31, EnergyAddedKWh: 100,
	})
	if ok {
		t.Fatal("want reject for fit > 250 kWh")
	}
}
