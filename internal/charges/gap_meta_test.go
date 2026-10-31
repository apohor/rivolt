package charges

import "testing"

// The metadata an upsert merges carries only the marks that are set, so a re-save without them leaves the stored ones alone.
func TestChargeMetaDoc(t *testing.T) {
	for _, c := range []struct {
		in   Charge
		want string
	}{
		{Charge{}, `{}`},
		{Charge{EnergyEstimated: true}, `{"energy_estimated":true}`},
		{Charge{GapKWh: 1.23456}, `{"gap_kwh":1.235}`},
		{Charge{EnergyEstimated: true, GapKWh: 2}, `{"energy_estimated":true,"gap_kwh":2}`},
	} {
		if got := chargeMetaDoc(c.in); got != c.want {
			t.Errorf("chargeMetaDoc(%+v) = %s, want %s", c.in, got, c.want)
		}
	}
}
