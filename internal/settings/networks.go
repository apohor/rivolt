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
	return normalizeNetworks(out), nil
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
		row := ChargingNetwork{
			Name:        n.DisplayName,
			PricePerKWh: n.GuestRate,
			Currency:    DefaultHomeCurrency,
			Slug:        n.Slug,
		}
		if n.MemberRate != nil {
			row.MemberPrice = *n.MemberRate
		}
		row.MemberPlan = n.MemberPlan
		out = append(out, row)
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
func normalizeNetworks(in []ChargingNetwork) []ChargingNetwork {
	out := make([]ChargingNetwork, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, n := range in {
		name := strings.TrimSpace(n.Name)
		if name == "" || n.PricePerKWh <= 0 {
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
			MemberPlan:  strings.TrimSpace(n.MemberPlan),
			Slug:        strings.TrimSpace(n.Slug),
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
