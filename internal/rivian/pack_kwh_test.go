package rivian

import "testing"

// The usable figures per pack (see InferPackKWh for where they come from), and
// the two values an earlier version had wrong: Max was 180 kWh (neither usable
// 141 nor gross 149) and the Gen 2 Large was the Max pack's 141.5.
func TestInferPackKWhUsableFigures(t *testing.T) {
	for name, c := range map[string]struct {
		model, trim string
		year        int
		want        float64
	}{
		"gen1 max":             {"R1T", "MAX-DM", 2023, 141},
		"gen1 max quad":        {"R1S", "MAX-QM", 2024, 141},
		"gen2 max":             {"R1T", "G2-MAX-DM", 2025, 141.5},
		"gen1 large":           {"R1T", "LRG-DM-PRFM", 2022, 131},
		"gen2 large":           {"R1S", "G2-LRG-DM", 2025, 109.4},
		"gen1 standard":        {"R1T", "STD-DM", 2022, 105},
		"gen1 standard+":       {"R1T", "STD-PLUS", 2023, 121},
		"gen2 standard":        {"R1T", "G2-STD-DM", 2025, 92.5},
		"launch edition":       {"R1T", "PKG-LCH", 2022, 131},
		"gen1 no pack in trim": {"R1S", "PKG-ADV", 2023, 131},
		"gen2 no pack in trim": {"R1T", "BLD-DR1T", 2025, 141.5},
		"r2":                   {"R2", "BLD-PRF2", 2027, 91.5},
		"unknown":              {"X", "", 2020, DefaultPackKWh},
	} {
		if got := InferPackKWh(c.model, c.trim, c.year); got != c.want {
			t.Errorf("%s: InferPackKWh(%q, %q, %d) = %v, want %v", name, c.model, c.trim, c.year, got, c.want)
		}
	}
	for _, trim := range []string{"MAX-DM", "G2-MAX-DM", "LRG-QM", "G2-LRG-DM", "STD-DM"} {
		if got := InferPackKWh("R1T", trim, 2024); got >= 150 {
			t.Errorf("%s infers %v kWh, more than any Rivian pack's gross capacity", trim, got)
		}
	}
}

// A pack is "known" only when the trim names it (or the model has one pack). The
// real prod trims carry no pack token, so their figure is a guess that must not be
// shown as a spec beside the truck's own reported capacity.
func TestInferPackKWhKnownOnlyWhenThePackIsIdentified(t *testing.T) {
	for name, c := range map[string]struct {
		model, trim string
		year        int
		known       bool
	}{
		"PKG-ADV gen1":   {"R1T", "PKG-ADV", 2023, false},
		"PKG-LCH":        {"R1T", "PKG-LCH", 2022, false},
		"BLD-DR1T gen2":  {"R1T", "BLD-DR1T", 2025, false},
		"BLD-TR1S gen2":  {"R1S", "BLD-TR1S", 2026, false},
		"unknown model":  {"X", "", 2020, false},
		"max token":      {"R1T", "MAX-QM", 2023, true},
		"gen2 large":     {"R1S", "G2-LRG-DM", 2025, true},
		"gen1 standard":  {"R1T", "STD-DM", 2022, true},
		"r2":             {"R2", "BLD-PRF2", 2027, true},
		"r2 unseen trim": {"R2", "BLD-STD2", 2027, false},
		"r2 no trim":     {"R2", "", 2027, false},
	} {
		if _, known := InferPackKWhKnown(c.model, c.trim, c.year); known != c.known {
			t.Errorf("%s: known = %v, want %v", name, known, c.known)
		}
	}
}
