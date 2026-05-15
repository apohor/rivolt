package tripadvice

import "strings"

// Network is one DCFC operator with a guest rate and, where it
// exists, a member rate. The "is the user a member?" question is
// deliberately not modeled here - we show BOTH totals on the
// planner and let the user do the comparison. Keeps the data model
// trivial (no settings panel, no migration) and the savings line
// reads as a one-glance number.
//
// Rates are USD/kWh for US deployments at time of writing
// (mid-2025). They will go stale - Rivolt's user-history pricing
// (deferred slice C) is the long-term fix; this table is the
// best-effort fallback when we have no per-user data for that
// network yet.
type Network struct {
	Slug          string
	DisplayName   string
	// MatchPatterns are substrings checked (case-insensitive) against
	// the charger name returned by Rivian's planTrip waypoint.
	// Order matters: more specific patterns must come first. The
	// Tesla / Tesla Supercharger pair is the canonical example -
	// "tesla supercharger" appears before "tesla" so destination
	// chargers don't accidentally claim the Supercharger rate.
	MatchPatterns []string
	GuestRate     float64
	// MemberRate is the rate after paying for the network's
	// subscription. Nil = no membership tier exists (ChargePoint,
	// Francis, etc.) or the user is automatically a member (RAN -
	// every Rivolt user is by definition a Rivian owner).
	MemberRate *float64
	// MemberPlan is the human-readable plan name + monthly cost
	// surfaced in the cost-strip tooltip later. Empty when no plan.
	MemberPlan string
}

func f64(v float64) *float64 { return &v }

// Networks is the lookup table, ordered most-specific-first so the
// substring matcher returns the right hit. Last entry (slug
// "unmatched") is the fallthrough - its patterns never match, but
// MatchNetwork returns it when nothing else does.
var Networks = []Network{
	{
		Slug:          "ea",
		DisplayName:   "Electrify America",
		MatchPatterns: []string{"electrify america"},
		GuestRate:     0.48,
		MemberRate:    f64(0.36),
		MemberPlan:    "Pass+ - $7/mo",
	},
	{
		Slug:          "tesla_sc",
		DisplayName:   "Tesla Supercharger",
		MatchPatterns: []string{"tesla supercharger", "supercharger"},
		GuestRate:     0.55,
		MemberRate:    f64(0.40),
		MemberPlan:    "Supercharging Membership - $12.99/mo",
	},
	{
		Slug:          "ran",
		DisplayName:   "Rivian Adventure Network",
		MatchPatterns: []string{"rivian adventure", "adventure network", "rivian ran"},
		GuestRate:     0.45, // Rivian owner rate; non-owners can't use it.
		MemberRate:    nil,
		MemberPlan:    "",
	},
	{
		Slug:          "evgo",
		DisplayName:   "EVgo",
		MatchPatterns: []string{"evgo"},
		GuestRate:     0.42,
		MemberRate:    f64(0.34),
		MemberPlan:    "Rewards+ - $6.99/mo",
	},
	{
		Slug:          "blink",
		DisplayName:   "Blink",
		MatchPatterns: []string{"blink"},
		GuestRate:     0.49,
		MemberRate:    f64(0.39),
		MemberPlan:    "Blink Member - annual",
	},
	{
		Slug:          "bp_pulse",
		DisplayName:   "bp pulse",
		MatchPatterns: []string{"bp pulse", "volta"},
		GuestRate:     0.45,
		MemberRate:    f64(0.39),
		MemberPlan:    "bp pulse Plus - $4/mo",
	},
	{
		Slug:          "shell_recharge",
		DisplayName:   "Shell Recharge",
		MatchPatterns: []string{"shell recharge", "shell ev", "greenlots"},
		GuestRate:     0.43,
		MemberRate:    f64(0.40),
		MemberPlan:    "GO+ - free",
	},
	{
		Slug:          "chargepoint",
		DisplayName:   "ChargePoint",
		MatchPatterns: []string{"chargepoint"},
		GuestRate:     0.45,
		MemberRate:    nil,
		MemberPlan:    "",
	},
	{
		Slug:          "francis_energy",
		DisplayName:   "Francis Energy",
		MatchPatterns: []string{"francis energy"},
		GuestRate:     0.40,
		MemberRate:    nil,
		MemberPlan:    "",
	},
	{
		Slug:          "ionna",
		DisplayName:   "Ionna",
		MatchPatterns: []string{"ionna"},
		GuestRate:     0.40,
		MemberRate:    nil,
		MemberPlan:    "",
	},
	{
		Slug:          "flo",
		DisplayName:   "Flo",
		MatchPatterns: []string{"flo charging", " flo "},
		GuestRate:     0.35,
		MemberRate:    nil,
		MemberPlan:    "",
	},
}

// UnmatchedNetwork is the fallback when MatchNetwork can't pin a
// stop to a known operator. Pinned at "unknown" rather than guessing
// - every per-stop quote should be traceable to a row in this file.
var UnmatchedNetwork = Network{
	Slug:        "unmatched",
	DisplayName: "Unknown DCFC",
	GuestRate:   0.46,
	MemberRate:  nil,
	MemberPlan:  "",
}

// MatchNetwork returns the network entry whose first MatchPattern
// appears (case-insensitive) in the charger name. Falls back to
// UnmatchedNetwork - caller never has to nil-check.
//
// The match is on the bare charger name as Rivian's planner returned
// it (e.g. "Electrify America - Pflugerville Crossing"). Whitespace
// around the user-visible name varies; lowercasing both sides plus
// padding the haystack with a space on each end lets " flo "-style
// boundary patterns work without a regex.
func MatchNetwork(name string) Network {
	hay := " " + strings.ToLower(strings.TrimSpace(name)) + " "
	for _, n := range Networks {
		for _, p := range n.MatchPatterns {
			if strings.Contains(hay, p) {
				return n
			}
		}
	}
	return UnmatchedNetwork
}

// MemberRateOrGuest returns the network's MemberRate when set,
// otherwise its GuestRate. Used by the cost estimator's
// "with-all-memberships" total - networks that don't have a
// membership simply contribute their guest rate to both totals.
func (n Network) MemberRateOrGuest() float64 {
	if n.MemberRate != nil {
		return *n.MemberRate
	}
	return n.GuestRate
}
