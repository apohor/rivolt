package settings

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// keyChargingNetworks holds the user's price book for fast/public
// charging networks. Stored as a single JSON blob in the same KV
// table as the rest of the charging config so it migrates and
// backs up with everything else.
const keyChargingNetworks = "charging.networks"

// ChargingNetwork is one entry in the price book. Two use cases share
// this row:
//
//  1. Manual charge-session pricing on the Charges page (one-click
//     "apply EA rate" for a recorded session). Uses Name +
//     PricePerKWh + Currency.
//  2. Trip planner per-stop cost estimation. Adds MemberPrice +
//     MemberActive so the planner can quote both "guest" and "your
//     actual" rates without us tracking memberships behind the
//     scenes. Slug ties a row to the built-in dcfcrates.Networks
//     table for match-pattern lookup (so a charger name like
//     "Electrify America - Pflugerville" still maps to the EA row
//     without the user re-entering patterns).
//
// New fields are all optional with omitempty so the manual-pricing
// UI keeps deserialising older JSON blobs unchanged.
type ChargingNetwork struct {
	Name         string  `json:"name"`
	PricePerKWh  float64 `json:"price_per_kwh"`
	Currency     string  `json:"currency"`
	MemberPrice  float64 `json:"member_price_per_kwh,omitempty"`
	MemberActive bool    `json:"member_active,omitempty"`
	MemberPlan   string  `json:"member_plan,omitempty"`
	Slug         string  `json:"slug,omitempty"`
	// Preferred = "rank this network high in Rivian's planner".
	// Forwarded as networkPreferences[].preference=1 on plan
	// requests. Unset / false sends nothing for that network.
	Preferred bool `json:"preferred,omitempty"`
	// Seeded is computed on read, never stored: the row's rates are still the built-in
	// table's (or blank), so the user has not entered a rate for this network. Its
	// price fields are returned as 0 ("unset"): the table's mid-2025 figures are dated
	// estimates that no network publishes as a price and are not shown or applied as
	// one. A row the user edited has Seeded false.
	Seeded bool `json:"seeded,omitempty"`
}

// GetChargingNetworks returns the configured price book. When the
// store has no entry yet, returns the seed defaults from
// dcfcrates.Networks so the Settings UI always has something to
// show on first load. The seed result is NOT persisted - the user
// has to hit Save once before their list diverges from the seed.
func GetChargingNetworks(ctx context.Context, s *Store) ([]ChargingNetwork, error) {
	if s == nil {
		return nil, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	raw, ok := all[keyChargingNetworks]
	if !ok || raw == "" {
		return DefaultChargingNetworks(), nil
	}
	var out []ChargingNetwork
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return DefaultChargingNetworks(), nil
	}
	return markSeeded(normalizeNetworks(out)), nil
}

// markSeeded treats a built-in network row whose rates are still the table's (an
// account that saved the book while the table's numbers were the seed) as unset: its
// prices are returned as 0 and Seeded is set. Nothing is rewritten in the store. A row
// with any other rate, and every custom row, is the user's own.
func markSeeded(rows []ChargingNetwork) []ChargingNetwork {
	for i, r := range rows {
		base, ok := builtinBySlug(r.Slug)
		if !ok {
			continue
		}
		defMember := 0.0
		if base.MemberRate != nil {
			defMember = *base.MemberRate
		}
		if r.PricePerKWh == 0 || (r.PricePerKWh == base.GuestRate && (r.MemberPrice == 0 || r.MemberPrice == defMember)) {
			rows[i].PricePerKWh, rows[i].MemberPrice, rows[i].Seeded = 0, 0, true
		}
	}
	return rows
}

func builtinBySlug(slug string) (dcfcrates.Network, bool) {
	for _, n := range dcfcrates.Networks {
		if n.Slug == slug && slug != "" {
			return n, true
		}
	}
	return dcfcrates.Network{}, false
}

// SetChargingNetworks persists the provided price book, overwriting
// any prior value. Empty names and non-positive prices are dropped
// silently so a partially filled UI form can't corrupt the list.
// Pass an empty slice to clear the user's overrides; the next Get
// will return the defaults again.
func SetChargingNetworks(ctx context.Context, s *Store, networks []ChargingNetwork) error {
	if s == nil {
		return nil
	}
	clean := normalizeNetworks(networks)
	b, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return s.Set(ctx, keyChargingNetworks, string(b))
}

// ResetChargingNetworks clears the user's persisted overrides so the
// next Get returns the seed defaults. Drives the "Reset to defaults"
// button on the Settings panel.
func ResetChargingNetworks(ctx context.Context, s *Store) error {
	if s == nil {
		return nil
	}
	return s.Set(ctx, keyChargingNetworks, "")
}

// DefaultChargingNetworks returns the seed list derived from
// dcfcrates.Networks. Same order as the built-in table so the
// Settings panel renders predictably across reloads.
func DefaultChargingNetworks() []ChargingNetwork {
	out := make([]ChargingNetwork, 0, len(dcfcrates.Networks))
	for _, n := range dcfcrates.Networks {
		// Blank rates: the table's figures are dated estimates, not the user's price.
		row := ChargingNetwork{
			Name:         n.DisplayName,
			Currency:     DefaultHomeCurrency,
			Slug:         n.Slug,
			MemberActive: n.DefaultMemberActive,
			Seeded:       true,
		}
		row.MemberPlan = n.MemberPlan
		out = append(out, row)
	}
	return out
}

// NetworkPref is one entry in a Rivian networkPreferences[] array.
// Defined here (not in rivian/) because settings is imported by
// rivian; the api layer maps these to rivian.NetworkPreference.
type NetworkPref struct {
	NetworkID  string
	Preference int
}

// NetworkPreferenceList returns Rivian networkId entries for the
// networks the user marked Preferred. Empty when the user picked
// none — caller then omits networkPreferences entirely.
//
// Wire shape mirrors the Rivian Android app (3.6 + 3.12 confirmed
// via apktool): NetworkPreferencesInput has a single `networkId`
// field; no `preference` value is sent. The app submits ONLY the
// preferred IDs; networks omitted from the list are not treated as
// "deprioritise" but as "no opinion." Preference=1 is included on
// the Go-side struct so the rivian client still has a value, but
// the GraphQL serializer is what determines whether it goes on the
// wire.
//
// Networks span multiple operator IDs (RAN = RAN+RWN; FLO =
// FLOC+FLOU); all share the user's per-slug toggle and we emit one
// row per ID.
func NetworkPreferenceList(networks []ChargingNetwork) []NetworkPref {
	out := make([]NetworkPref, 0, len(networks))
	seen := make(map[string]bool)
	bySlug := make(map[string]dcfcrates.Network, len(dcfcrates.Networks))
	for _, n := range dcfcrates.Networks {
		bySlug[n.Slug] = n
	}
	for _, n := range networks {
		if !n.Preferred {
			continue
		}
		base, ok := bySlug[n.Slug]
		if !ok {
			continue
		}
		for _, id := range base.RivianIDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, NetworkPref{NetworkID: id, Preference: 1})
		}
	}
	return out
}

// AsOverrides converts a persisted network list into the
// slug-keyed override slice the tripadvice cost estimator consumes.
// Custom user-added rows (no Slug) are passed through with their
// Name so the substring matcher in tripadvice can still hit them.
func AsOverrides(networks []ChargingNetwork) []dcfcrates.NetworkOverride {
	out := make([]dcfcrates.NetworkOverride, 0, len(networks))
	for _, n := range networks {
		out = append(out, dcfcrates.NetworkOverride{
			Slug:         n.Slug,
			Name:         n.Name,
			GuestRate:    n.PricePerKWh,
			MemberRate:   n.MemberPrice,
			MemberActive: n.MemberActive,
			MemberPlan:   n.MemberPlan,
		})
	}
	return out
}

// normalizeNetworks trims whitespace, drops invalid rows, defaults
// the currency to USD where missing, and sorts alphabetically so
// the UI surfaces a stable list across reloads.
// retiredPlanNames are plan names the built-in table used to carry and no longer does,
// because they were wrong or unsourced (Oct 2026): EVgo's plan is "EVgo Plus", Shell's US
// programme is "Shell Recharge Rewards" (GO+ is a UK one), and Rivian states no
// percentage for the owner discount. A user who saved their price book while the old
// text was the default holds a stored copy of it, which a change to the table does not
// reach. Reading and saving the book swaps exactly these strings for the current ones;
// any other text a user wrote is left alone (an overlay by slug would overwrite it).
var retiredPlanNames = map[string]string{
	"Rivian owner (10% off)": "Rivian owner discount",
	"Rewards+ - $6.99/mo":    "EVgo Plus - $6.99/mo",
	"GO+ - free":             "Recharge Rewards - free tier",
}

func currentPlanName(plan string) string {
	if cur, ok := retiredPlanNames[plan]; ok {
		return cur
	}
	return plan
}

func normalizeNetworks(in []ChargingNetwork) []ChargingNetwork {
	out := make([]ChargingNetwork, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, n := range in {
		name := strings.TrimSpace(n.Name)
		// A custom row needs a positive price (a half-filled form is dropped); a built-in
		// network's row may be blank: that is "no rate entered yet".
		if _, builtin := builtinBySlug(strings.TrimSpace(n.Slug)); name == "" || n.PricePerKWh <= 0 && !builtin {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		cur := strings.TrimSpace(n.Currency)
		if cur == "" {
			cur = DefaultHomeCurrency
		}
		row := ChargingNetwork{
			Name:        name,
			PricePerKWh: n.PricePerKWh,
			Currency:    cur,
			MemberPlan:  currentPlanName(strings.TrimSpace(n.MemberPlan)),
			Slug:        strings.TrimSpace(n.Slug),
			Preferred:   n.Preferred,
		}
		// Member price must be positive AND below guest to count -
		// matches the sanity test in tripadvice/networks_test.go.
		if n.MemberPrice > 0 && n.MemberPrice < n.PricePerKWh {
			row.MemberPrice = n.MemberPrice
			row.MemberActive = n.MemberActive
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
