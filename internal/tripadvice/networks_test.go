package tripadvice

import "testing"

// TestMatchNetwork_RealRivianNames pins the strings Rivian's planner
// actually returns (sampled from real plans). The pivot points:
//
//   - "Tesla Supercharger - X" must match tesla_sc, NOT a hypothetical
//     "Tesla Destination" L2 site (we'd never plan a DCFC stop at L2,
//     but the substring "tesla" alone would be ambiguous).
//   - "Rivian Adventure Network" / "Rivian RAN" / "RAN" all map to
//     the same row.
//   - "Electrify America - <site>" with the en-dash separator.
//   - Plain "ChargePoint Network" without a city suffix.
//
// Regressions on any of these silently push the affected stop to
// UnmatchedNetwork at $0.46/kWh - the cost strip would still render,
// just with the wrong number, so explicit assertion is the only
// safety net.
func TestMatchNetwork_RealRivianNames(t *testing.T) {
	cases := []struct {
		name string
		slug string
	}{
		{"Electrify America - Pflugerville Crossing", "ea"},
		{"electrify america", "ea"},
		{"Tesla Supercharger - Austin Domain", "tesla_sc"},
		{"Supercharger - I-10 Junction", "tesla_sc"},
		{"Rivian Adventure Network - Bastrop", "ran"},
		{"Rivian RAN - Austin", "ran"},
		{"EVgo - Round Rock", "evgo"},
		{"Blink - Capitol Plaza", "blink"},
		{"bp pulse - Houston", "bp_pulse"},
		{"Volta - Westlake", "bp_pulse"},
		{"Shell Recharge - I-35", "shell_recharge"},
		{"Greenlots - Legacy Site", "shell_recharge"},
		{"ChargePoint Network", "chargepoint"},
		{"Francis Energy - Tulsa", "francis_energy"},
		{"Ionna - Charlotte Pilot", "ionna"},
		{"Flo Charging - Quebec", "flo"},
		{"Some No-Name DCFC Co", "unmatched"},
	}
	for _, c := range cases {
		got := MatchNetwork(c.name)
		if got.Slug != c.slug {
			t.Errorf("MatchNetwork(%q) = %q, want %q", c.name, got.Slug, c.slug)
		}
	}
}

// TestMatchNetwork_Ordering is the specificity guard. "tesla
// supercharger" must be tried before generic "tesla" / "supercharger"
// patterns; the test pins the order by feeding a name that would
// match both a specific and a less-specific pattern, and asserting
// the specific one wins.
func TestMatchNetwork_Ordering(t *testing.T) {
	// "tesla supercharger" pattern is listed in tesla_sc; if a
	// future refactor demotes it below a "tesla" generic pattern
	// the match flips.
	got := MatchNetwork("Tesla Supercharger - Generic")
	if got.Slug != "tesla_sc" {
		t.Errorf("specificity broken: got %q", got.Slug)
	}
}

// TestMemberRateOrGuest: networks without a member tier fall back to
// guest rate in the "with-memberships" total - they don't get
// double-counted at zero.
func TestMemberRateOrGuest(t *testing.T) {
	cases := []struct {
		slug string
		want float64
	}{
		{"ea", 0.36},        // has member tier
		{"tesla_sc", 0.40},  // has member tier
		{"ran", 0.45},       // no membership; fall through to guest
		{"chargepoint", 0.45},
		{"unmatched", 0.46},
	}
	all := append([]Network{}, Networks...)
	all = append(all, UnmatchedNetwork)
	by := map[string]Network{}
	for _, n := range all {
		by[n.Slug] = n
	}
	for _, c := range cases {
		got := by[c.slug].MemberRateOrGuest()
		if got != c.want {
			t.Errorf("%s.MemberRateOrGuest() = %v want %v", c.slug, got, c.want)
		}
	}
}

// TestRateTableSanity catches table-edit regressions: every member
// rate must be lower than the corresponding guest rate (paying for a
// membership that costs MORE makes no sense), and every rate must be
// positive and < $2/kWh (anything outside that range is a typo).
func TestRateTableSanity(t *testing.T) {
	for _, n := range Networks {
		if n.GuestRate <= 0 || n.GuestRate >= 2 {
			t.Errorf("%s guest rate out of plausible range: %v", n.Slug, n.GuestRate)
		}
		if n.MemberRate != nil && *n.MemberRate >= n.GuestRate {
			t.Errorf("%s member rate %v ≥ guest %v - table edit error", n.Slug, *n.MemberRate, n.GuestRate)
		}
	}
	if UnmatchedNetwork.GuestRate <= 0 || UnmatchedNetwork.GuestRate >= 2 {
		t.Errorf("unmatched guest rate out of range: %v", UnmatchedNetwork.GuestRate)
	}
}
