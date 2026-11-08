package rivian

import "testing"

// FrameStillCharging is stillChargingFrame behind a charging_* state, for the open-charge gauge: it must agree with the janitor's rule.
func TestFrameStillCharging(t *testing.T) {
	for _, c := range []struct {
		name, state, status string
		kw                  float64
		gear                string
		want                bool
	}{
		{"plugged and charging", "charging_active", "chrgr_sts_connected_charging", 7.2, "P", true},
		{"plugged, ready, 0 kW", "charging_ready", "chrgr_sts_connected_no_chrg", 0, "P", true},
		{"charger state stuck after an unplug", "charging_active", "chrgr_sts_not_connected", 7.2, "P", false},
		{"plugged but in a driving gear", "charging_active", "chrgr_sts_connected_charging", 7.2, "D", false},
		{"a terminal state", "charging_complete", "chrgr_sts_connected_no_chrg", 0, "P", false},
		{"no plug status, power flowing (an R2)", "charging_active", "", 7.2, "P", true},
		{"no plug status, no power", "charging_active", "", 0, "P", false},
		{"nothing", "", "", 0, "", false},
	} {
		if got := FrameStillCharging(c.state, c.status, c.kw, c.gear); got != c.want {
			t.Errorf("%s: FrameStillCharging(%q,%q,%v,%q) = %v, want %v", c.name, c.state, c.status, c.kw, c.gear, got, c.want)
		}
	}
}
