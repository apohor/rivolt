package settings

import "testing"

// A price book saved before the plan names were corrected holds the stored copy of the
// old text; reading it (and saving it) swaps exactly the retired strings, nothing else.
func TestNormalizeNetworksRenamesRetiredPlanNames(t *testing.T) {
	in := []ChargingNetwork{
		{Name: "Rivian Adventure Network", PricePerKWh: 0.6, MemberPrice: 0.54, MemberActive: true, MemberPlan: "Rivian owner (10% off)", Slug: "ran"},
		{Name: "EVgo", PricePerKWh: 0.42, MemberPrice: 0.34, MemberPlan: " Rewards+ - $6.99/mo ", Slug: "evgo"},
		{Name: "Shell Recharge", PricePerKWh: 0.43, MemberPrice: 0.40, MemberPlan: "GO+ - free", Slug: "shell_recharge"},
		{Name: "Shell Again", PricePerKWh: 0.43, MemberPlan: "Recharge Rewards - free tier"},
		{Name: "My Local Network", PricePerKWh: 0.30, MemberPlan: "Rewards+ at my gym, $5/mo"}, // a user's own text, kept
		{Name: "Electrify America", PricePerKWh: 0.48, MemberPrice: 0.36, MemberPlan: "Pass+ - $7/mo", Slug: "ea"},
	}
	got := map[string]string{}
	for _, n := range normalizeNetworks(in) {
		got[n.Name] = n.MemberPlan
	}
	want := map[string]string{
		"Rivian Adventure Network": "Rivian owner discount",
		"EVgo":                     "EVgo Plus - $6.99/mo",
		"Shell Recharge":           "Shell Recharge Rewards - free tier",
		"Shell Again":              "Shell Recharge Rewards - free tier",
		"My Local Network":         "Rewards+ at my gym, $5/mo",
		"Electrify America":        "Pass+ - $7/mo",
	}
	for name, plan := range want {
		if got[name] != plan {
			t.Errorf("%s: plan %q, want %q", name, got[name], plan)
		}
	}
}

// The seeded defaults carry the current names, never a retired one.
func TestDefaultChargingNetworksCarryNoRetiredPlanName(t *testing.T) {
	for _, n := range DefaultChargingNetworks() {
		if _, retired := retiredPlanNames[n.MemberPlan]; retired {
			t.Errorf("%s still seeds the retired plan name %q", n.Name, n.MemberPlan)
		}
	}
}
