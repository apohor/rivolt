package rivian

import "testing"

func TestFieldSupport(t *testing.T) {
	r1 := FieldSupport("R1S")
	r2 := FieldSupport("r2 ")
	for field, want := range map[string][2]string{
		"charge_time_remaining": {FieldReported, FieldEstimate},
		"power_state":           {FieldReported, FieldBestEffort},
		"trip_target":           {FieldReported, FieldUnavailable},
		"outside_temp":          {FieldUnavailable, FieldUnavailable},
		"charge_stop_reason":    {FieldUnavailable, FieldUnavailable},
		"user_modes":            {FieldUnavailable, FieldUnavailable},
		"parked_energy":         {FieldUnavailable, FieldUnavailable},
		"smart_charging":        {FieldUnavailable, FieldUnavailable},
	} {
		if r1[field] != want[0] || r2[field] != want[1] {
			t.Errorf("%s: R1 %q R2 %q, want %q / %q", field, r1[field], r2[field], want[0], want[1])
		}
	}
	// A copy each time, and an unknown model is treated as an R1.
	r1["power_state"] = "changed"
	if FieldSupport("R1T")["power_state"] != FieldReported || FieldSupport("")["power_state"] != FieldReported {
		t.Error("FieldSupport shared its map, or an unknown model is not an R1")
	}
}
