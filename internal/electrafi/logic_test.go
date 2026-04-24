package electrafi

import (
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestParseElectrafiTime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load ny: %v", err)
	}
	const raw = "2026-03-15 09:00:00"
	const winter = "2026-01-15 09:00:00" // EST, before DST spring-forward
	for _, tc := range []struct {
		name   string
		in     string
		loc    *time.Location
		wantRF string // expected UTC RFC3339
	}{
		{"utc", raw, time.UTC, "2026-03-15T09:00:00Z"},
		{"ny_edt", raw, ny, "2026-03-15T13:00:00Z"},     // EDT = UTC-4
		{"ny_est", winter, ny, "2026-01-15T14:00:00Z"},  // EST = UTC-5
		{"nil_loc_defaults_to_utc", raw, nil, "2026-03-15T09:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseElectrafiTime(tc.in, tc.loc)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.UTC().Format(time.RFC3339) != tc.wantRF {
				t.Fatalf("got %s want %s", got.UTC().Format(time.RFC3339), tc.wantRF)
			}
		})
	}
	if _, err := parseElectrafiTime("", time.UTC); err == nil {
		t.Fatalf("expected error for empty input")
	}
}

func TestInferPackKWh(t *testing.T) {
	for _, tc := range []struct {
		name      string
		model     string
		trim      string
		modelYear int
		want      float64
	}{
		{"gen1_large", "R1T", "LRG-DM-STD", 2023, 131.0},
		{"gen1_large_performance", "R1S", "LRG-DM-PRFM", 2024, 131.0},
		{"gen2_large_by_prefix", "R1S", "G2-LRG-DM", 2025, 141.5},
		{"gen1_trim_but_2025_is_gen2", "R1T", "LRG-DM-STD", 2025, 141.5},
		{"max_pack", "R1T", "MAX-QM", 2023, 180.0},
		{"gen2_standard_plus", "R1T", "G2-STD-DM", 2025, 92.5},
		{"gen1_standard", "R1T", "STD-DM", 2023, 105.0},
		{"pkg_adv_gen1_falls_back_to_large", "R1T", "PKG-ADV", 2023, 131.0},
		{"pkg_adv_gen2_falls_back_to_large", "R1T", "PKG-ADV", 2025, 141.5},
		{"r2", "R2", "", 2027, 75.0},
		{"unknown", "", "", 0, rivian.DefaultPackKWh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rivian.InferPackKWh(tc.model, tc.trim, tc.modelYear); got != tc.want {
				t.Fatalf("got %g want %g", got, tc.want)
			}
		})
	}
}

func TestDeriveChargeSoCFallback(t *testing.T) {
	// Simulate the post-2026-03-24 ElectraFi regression: charger_power
	// and charge_energy_added are both empty, so the importer must
	// back out energy from SoC delta * packKWh and power from
	// energy / elapsed time.
	start := time.Date(2026, 4, 1, 22, 0, 0, 0, time.UTC)
	// 10% -> 90% over 8 hours on a 131 kWh pack = 104.8 kWh, ~13.1 kW avg.
	snaps := []snapshot{
		{at: start, batteryLevel: 10},
		{at: start.Add(4 * time.Hour), batteryLevel: 50},
		{at: start.Add(8 * time.Hour), batteryLevel: 90},
	}
	c := deriveCharge("id", "vid", snaps, 131.0)

	if math.Abs(c.EnergyAddedKWh-104.8) > 1e-6 {
		t.Fatalf("energy: got %g want 104.8", c.EnergyAddedKWh)
	}
	if math.Abs(c.AvgPowerKW-13.1) > 1e-6 {
		t.Fatalf("avg power: got %g want 13.1", c.AvgPowerKW)
	}
	if c.MaxPowerKW != c.AvgPowerKW {
		t.Fatalf("max power should equal avg in fallback, got max=%g avg=%g", c.MaxPowerKW, c.AvgPowerKW)
	}
	if c.StartSoCPct != 10 || c.EndSoCPct != 90 {
		t.Fatalf("soc bounds: %v/%v", c.StartSoCPct, c.EndSoCPct)
	}

	// Real charger_power present: fallback must not engage, avg/max
	// should come from the reported readings.
	snaps2 := []snapshot{
		{at: start, batteryLevel: 10, chargerPowerKW: 150, chargeEnergyKWh: 0},
		{at: start.Add(30 * time.Minute), batteryLevel: 50, chargerPowerKW: 200, chargeEnergyKWh: 60},
		{at: start.Add(45 * time.Minute), batteryLevel: 70, chargerPowerKW: 120, chargeEnergyKWh: 90},
	}
	c2 := deriveCharge("id2", "vid", snaps2, 131.0)
	if c2.MaxPowerKW != 200 {
		t.Fatalf("max power: got %g want 200", c2.MaxPowerKW)
	}
	if c2.EnergyAddedKWh != 90 {
		t.Fatalf("energy: got %g want 90", c2.EnergyAddedKWh)
	}
}
