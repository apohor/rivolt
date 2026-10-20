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
	// RivianIDs are the values passed to Rivian's planTrip2
	// networkPreferences[].networkId field. Some networks span
	// multiple operator IDs (RAN = Adventure + Wayfinder; FLO =
	// FLOC + FLOU for Canada/US), so this is a list. Mapping of the
	// network ids Rivian reports:
	//
	//   10001, 10002 → RAN (Adventure + Wayfinder, "RAN, RWN")
	//   10025         → ChargePoint
	//   10027         → Electrify America
	//   10030         → EV Connect
	//   10031         → EVgo
	//   10032, 10033 → FLO ("FLOC, FLOU" — Canada + US)
	//   10045         → Shell Recharge
	//   10050         → Tesla
	//   10056         → IONNA
	//
	// Networks outside that enum (Blink, bp pulse, Francis Energy)
	// leave RivianIDs empty — Rivian's planner doesn't recognise
	// them, so sending an arbitrary ID would risk being treated as
	// "exclude everything else." Custom user-added rows also stay
	// empty.
	RivianIDs []string
	// DefaultMemberActive is the initial MemberActive state for a
	// freshly-seeded ChargingNetwork row. True for networks every
	// Rivolt user qualifies for by definition (RAN — every user
	// owns a Rivian). False for opt-in subscriptions (EA Pass+,
	// Tesla SC Membership, EVgo Plus).
	DefaultMemberActive bool
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
		RivianIDs:     []string{"10027"},
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
		RivianIDs:     []string{"10050"},
	},
	{
		Slug:          "ran",
		DisplayName:   "Rivian Adventure Network",
		MatchPatterns: []string{"rivian adventure", "adventure network", "rivian ran"},
		// RAN is Rivian-only — every Rivolt user qualifies for the
		// "Rivian owner" rate by definition, so MemberRate should
		// apply automatically (settings.AsOverrides defaults
		// MemberActive=true for RAN). Guest = walk-up sticker; the
		// Rivian-owner discount is the member tier. Rivian states no
		// percentage (pricing is shown in the app and at the charger), so
		// the plan name carries none; the member rate stays an estimate.
		GuestRate:           0.60,
		MemberRate:          f64(0.54),
		MemberPlan:          "Rivian owner discount",
		RivianIDs:           []string{"10001", "10002"},
		DefaultMemberActive: true,
	},
	{
		Slug:          "evgo",
		DisplayName:   "EVgo",
		MatchPatterns: []string{"evgo"},
		GuestRate:     0.42,
		MemberRate:    f64(0.34),
		MemberPlan:    "EVgo Plus - $6.99/mo",
		RivianIDs:     []string{"10031"},
	},
	{
		Slug:          "shell_recharge",
		DisplayName:   "Shell Recharge",
		MatchPatterns: []string{"shell recharge", "shell ev", "greenlots"},
		GuestRate:     0.43,
		MemberRate:    f64(0.40),
		MemberPlan:    "Recharge Rewards - free tier",
		RivianIDs:     []string{"10045"},
	},
	{
		Slug:          "chargepoint",
		DisplayName:   "ChargePoint",
		MatchPatterns: []string{"chargepoint"},
		GuestRate:     0.45,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianIDs:     []string{"10025"},
	},
	{
		Slug:          "ionna",
		DisplayName:   "Ionna",
		MatchPatterns: []string{"ionna"},
		GuestRate:     0.40,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianIDs:     []string{"10056"},
	},
	{
		Slug:          "flo",
		DisplayName:   "Flo",
		MatchPatterns: []string{"[flo]", "flo charging", " flo "},
		GuestRate:     0.35,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianIDs:     []string{"10032", "10033"},
	},
	{
		Slug:          "ev_connect",
		DisplayName:   "EV Connect",
		MatchPatterns: []string{"ev connect", "evconnect"},
		GuestRate:     0.40,
		MemberRate:    nil,
		MemberPlan:    "",
		RivianIDs:     []string{"10030"},
	},
}

// nrelNetworkNames maps NREL AFDC ev_network codes that no
// MatchPatterns substring catches as-is onto a name that does. Codes
// not listed are matched with underscores read as spaces.
var nrelNetworkNames = map[string]string{
	// A DCFC station on the Tesla network is a Supercharger
	// (Destination chargers are L2 and never reach DCFC pricing).
	"TESLA":            "Tesla Supercharger",
	"RIVIAN_ADVENTURE": "Rivian Adventure Network",
	"FLO":              "Flo Charging",
}

// StationQuery turns an NREL station's network code and name into the
// text ResolveRate matches, so a recorded DCFC session priced from the
// station it happened at resolves the same rows a planned stop does.
func StationQuery(nrelNetwork, stationName string) string {
	code := strings.TrimSpace(nrelNetwork)
	name, ok := nrelNetworkNames[strings.ToUpper(code)]
	if !ok {
		name = strings.ReplaceAll(code, "_", " ")
	}
	return strings.TrimSpace(name + " " + stationName)
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
	if ov, name, ok := matchOverride(stopName, overrides); ok {
		return resolvedFromOverride(ov, name)
	}
	// No override: the network is identified, but the built-in table's rate is NOT
	// returned. It is a dated estimate (mid-2025) that no network publishes as a
	// price, so it must not be stamped onto a recorded session or offered as "your
	// rate". Callers price only when the rates are positive, so an unset network
	// stays unpriced until the user enters what they pay.
	n := MatchNetwork(stopName)
	return ResolvedRate{Slug: n.Slug, DisplayName: n.DisplayName, MemberPlan: n.MemberPlan}
}

// matchOverride finds the user's override row for a stop: the first row with a
// positive rate whose slug's match patterns hit the stop name, or (for a slugless or
// unknown-slug custom row) whose Name is a substring of it. The display name is the
// built-in network's for a slugged row, the row's own for a custom one.
func matchOverride(stopName string, overrides []NetworkOverride) (NetworkOverride, string, bool) {
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
						return ov, base.DisplayName, true
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
			return ov, ov.Name, true
		}
	}
	return NetworkOverride{}, "", false
}

// StopNetwork is the network a planned stop belongs to, and the price the USER set
// for it, if they did. The built-in table's rates are deliberately not here: they are
// dated estimates (mid-2025) and the planner no longer shows them as dollar figures.
type StopNetwork struct {
	Slug        string
	DisplayName string
	MemberPlan  string
	// UserPerKWh is what the user's saved price book says they pay (their active
	// membership applied), and ok only when it is THEIR number: a custom row, or a
	// built-in row whose rate differs from the built-in default. A row still holding
	// the default (the seed the Settings page offers) is the table figure, not theirs.
	UserPerKWh float64
	UserSet    bool
}

// ResolveStop identifies a stop's network and the user's own price for it.
func ResolveStop(stopName string, overrides []NetworkOverride) StopNetwork {
	if ov, name, ok := matchOverride(stopName, overrides); ok {
		sn := StopNetwork{Slug: ov.Slug, DisplayName: name, MemberPlan: ov.MemberPlan}
		rate := ov.GuestRate
		if ov.MemberActive && ov.MemberRate > 0 && ov.MemberRate < ov.GuestRate {
			rate = ov.MemberRate
		}
		sn.UserPerKWh = rate
		base, known := networkBySlug(ov.Slug)
		switch {
		case ov.Slug == "" || !known:
			sn.UserSet = true // a custom row: every number in it is the user's
		default:
			defMember := 0.0
			if base.MemberRate != nil {
				defMember = *base.MemberRate
			}
			sn.UserSet = ov.GuestRate != base.GuestRate || ov.MemberRate != defMember
		}
		return sn
	}
	n := MatchNetwork(stopName)
	return StopNetwork{Slug: n.Slug, DisplayName: n.DisplayName, MemberPlan: n.MemberPlan}
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
