// Package dcfcrates owns the DCFC network table used by trip
// planning. It lives in its own package so the settings layer (which
// stores user-edited overrides) and the tripadvice layer (which
// consumes both) can share it without the settings -> tripadvice ->
// rivian -> settings import cycle that the older single-package
// layout produced.
package dcfcrates

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
	// RivianID is the value passed to Rivian's planTrip2
	// networkPreferences[].networkId field. Best-effort guess based
	// on the bracket form Rivian uses in their charger names
	// ("<location> [Tesla]" -> "Tesla"). Confirmed on the wire as
	// the spike rolls out; rows where the gateway rejects the ID
	// will get corrected here as we learn. Empty when we don't
	// have a guess (user-added custom rows).
	RivianID string
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
		RivianID:      "Electrify America",
	},
	{
		Slug:        "tesla_sc",
		DisplayName: "Tesla Supercharger",
		// Rivian's planner tags Tesla Supercharger stops as
		// "<location> [Tesla]" - the bracketed tag is the canonical
		// form we see in production responses, more specific than
		// the human-readable "Tesla Supercharger" string. Both
		// kept because operators forking the catalog may use
		// either shape; "supercharger" is the loose backup.
		MatchPatterns: []string{"[tesla]", "tesla supercharger", "supercharger"},
		GuestRate:     0.55,
		MemberRate:    f64(0.40),
		MemberPlan:    "Supercharging Membership - $12.99/mo",
		RivianID:      "Tesla",
	},
	{
		Slug:          "ran",
		DisplayName:   "Rivian Adventure Network",
		MatchPatterns: []string{"rivian adventure", "adventure network", "rivian ran"},
		GuestRate:     0.45, // Rivian owner rate; non-owners can't use it.
		MemberRate:    nil,
		MemberPlan:    "",
		RivianID:      "Rivian Adventure Network",
	},
	{
		Slug:          "evgo",
		DisplayName:   "EVgo",
		MatchPatterns: []string{"evgo"},
		GuestRate:     0.42,
		MemberRate:    f64(0.34),
		MemberPlan:    "Rewards+ - $6.99/mo",
		RivianID:      "EVgo",
	},
	{
		Slug:          "blink",
		DisplayName:   "Blink",
		MatchPatterns: []string{"blink"},
		GuestRate:     0.49,
		MemberRate:    f64(0.39),
		MemberPlan:    "Blink Member - annual",
		RivianID:      "Blink",
	},
	{
		Slug:          "bp_pulse",
		DisplayName:   "bp pulse",
		MatchPatterns: []string{"bp pulse", "volta"},
		GuestRate:     0.45,
		MemberRate:    f64(0.39),
		MemberPlan:    "bp pulse Plus - $4/mo",
		RivianID:      "bp pulse",
	},
	{
		Slug:          "shell_recharge",
		DisplayName:   "Shell Recharge",
		MatchPatterns: []string{"shell recharge", "shell ev", "greenlots"},
		GuestRate:     0.43,
		MemberRate:    f64(0.40),
		MemberPlan:    "GO+ - free",
		RivianID:      "Shell Recharge",
	},
	{
		Slug:          "chargepoint",
		DisplayName:   "ChargePoint",
		MatchPatterns: []string{"chargepoint"},
		GuestRate:     0.45,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianID:      "ChargePoint",
	},
	{
		Slug:          "francis_energy",
		DisplayName:   "Francis Energy",
		MatchPatterns: []string{"francis energy"},
		GuestRate:     0.40,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianID:      "Francis Energy",
	},
	{
		Slug:          "ionna",
		DisplayName:   "Ionna",
		MatchPatterns: []string{"ionna"},
		GuestRate:     0.40,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianID:      "Ionna",
	},
	{
		Slug:          "flo",
		DisplayName:   "Flo",
		MatchPatterns: []string{"[flo]", "flo charging", " flo "},
		GuestRate:     0.35,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianID:      "Flo",
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

// NetworkOverride is the bridge type the user-settings layer passes
// to the cost estimator so we can wire user-edited rates +
// per-membership-toggle into trip planning without tripadvice having
// to import the settings package. Slug ties the override to a row
// in the built-in Networks table so the match-pattern lookup keeps
// working; Name is the fallback substring for user-added custom
// rows that have no slug.
type NetworkOverride struct {
	Slug         string
	Name         string
	GuestRate    float64
	MemberRate   float64 // 0 = no member tier
	MemberActive bool
	MemberPlan   string
}

// ResolvedRate is what the cost estimator works with per stop.
// Three rates so the strip can show guest / your-memberships /
// max-if-you-joined-everything; DisplayName + Slug feed the
// breakdown attribution line.
type ResolvedRate struct {
	Slug         string
	DisplayName  string
	GuestRate    float64
	UserRate     float64 // honours the user's MemberActive toggle
	AllMemberRate float64 // hypothetical "you have every plan"
	MemberPlan   string
}

// ResolveRate picks the right rate for a planned stop. First the
// user's overrides (slug-keyed against the built-in match patterns,
// falling back to substring against Name for custom rows), then the
// built-in Networks defaults, then UnmatchedNetwork.
func ResolveRate(stopName string, overrides []NetworkOverride) ResolvedRate {
	hay := " " + strings.ToLower(strings.TrimSpace(stopName)) + " "
	// User overrides win. The lookup walks the override list, checks
	// each row's slug against the built-in match patterns, and
	// returns on first hit. Slugless custom rows match by substring
	// on Name so a user can add a one-off operator.
	for _, ov := range overrides {
		if ov.GuestRate <= 0 {
			continue
		}
		if ov.Slug != "" {
			if base, ok := networkBySlug(ov.Slug); ok {
				for _, p := range base.MatchPatterns {
					if strings.Contains(hay, p) {
						return resolvedFromOverride(ov, base.DisplayName)
					}
				}
				continue
			}
		}
		// Slugless / unknown-slug override: try the network's Name as
		// a substring. Lower-cased to match hay; padded so a single-
		// word name like "EA" doesn't accidentally match "TESLA".
		needle := " " + strings.ToLower(strings.TrimSpace(ov.Name)) + " "
		if needle != "  " && strings.Contains(hay, strings.TrimSpace(needle)) {
			return resolvedFromOverride(ov, ov.Name)
		}
	}
	// Fall back to the built-in defaults.
	n := MatchNetwork(stopName)
	rr := ResolvedRate{
		Slug:        n.Slug,
		DisplayName: n.DisplayName,
		GuestRate:   n.GuestRate,
		UserRate:    n.GuestRate, // user has no override, so no membership
		AllMemberRate: n.MemberRateOrGuest(),
		MemberPlan:  n.MemberPlan,
	}
	return rr
}

func networkBySlug(slug string) (Network, bool) {
	for _, n := range Networks {
		if n.Slug == slug {
			return n, true
		}
	}
	return Network{}, false
}

func resolvedFromOverride(ov NetworkOverride, displayName string) ResolvedRate {
	rr := ResolvedRate{
		Slug:        ov.Slug,
		DisplayName: displayName,
		GuestRate:   ov.GuestRate,
		UserRate:    ov.GuestRate,
		AllMemberRate: ov.GuestRate,
		MemberPlan:  ov.MemberPlan,
	}
	if ov.MemberRate > 0 && ov.MemberRate < ov.GuestRate {
		rr.AllMemberRate = ov.MemberRate
		if ov.MemberActive {
			rr.UserRate = ov.MemberRate
		}
	}
	return rr
}
