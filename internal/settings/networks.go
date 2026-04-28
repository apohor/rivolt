package settings

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// keyChargingNetworks holds the user's price book for fast/public
// charging networks. Stored as a single JSON blob in the same KV
// table as the rest of the charging config so it migrates and
// backs up with everything else.
const keyChargingNetworks = "charging.networks"

// ChargingNetwork is one entry in the price book — a friendly name
// (e.g. "EVgo", "Electrify America") plus a default $/kWh rate the
// UI can apply with one click when pricing a fast-charge session.
//
// We intentionally keep this flat: no per-tier pricing, no time-of-
// use rules. The goal is faster manual entry, not full automation.
type ChargingNetwork struct {
	Name          string  `json:"name"`
	PricePerKWh   float64 `json:"price_per_kwh"`
	Currency      string  `json:"currency"`
}

// GetChargingNetworks returns the configured price book, or an empty
// list when nothing is set. A malformed blob is treated as empty
// rather than an error so a bad value can be overwritten from the UI.
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
		return nil, nil
	}
	var out []ChargingNetwork
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, nil
	}
	return normalizeNetworks(out), nil
}

// SetChargingNetworks persists the provided price book, overwriting
// any prior value. Empty names and non-positive prices are dropped
// silently so a partially filled UI form can't corrupt the list.
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
		out = append(out, ChargingNetwork{
			Name:        name,
			PricePerKWh: n.PricePerKWh,
			Currency:    cur,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
