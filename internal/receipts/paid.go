package receipts

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// What the user actually paid per kWh at each public charging network, from their
// own Rivian receipts (the billed amount and the energy, not our estimate). The trip
// planner uses it before any third-party figure: it is this user's own price.

const (
	// PaidMinSessions is how many usable receipts a network needs before a figure is
	// shown; fewer is noise (one off-peak session, one promotion).
	PaidMinSessions = 3
	// PaidMaxSessions caps it at the most recent sessions, so an old price does not
	// outweigh the current one.
	PaidMaxSessions = 10
	// PaidMaxAge drops receipts older than this: a price paid long ago is not
	// today's price.
	PaidMaxAge = 365 * 24 * time.Hour
)

// PaidRate is one network's figure for one user.
type PaidRate struct {
	Slug     string
	Currency string
	// PerKWh is total paid over total energy across Sessions receipts (energy-weighted,
	// not an average of per-session prices).
	PerKWh   float64
	Sessions int
}

// PaidRates computes the figures from a user's receipts: per network (the vendor and
// charger name matched to the network table), over the most recent PaidMaxSessions
// receipts that are within PaidMaxAge, not at a home charger, in the given currency,
// with a positive amount and energy. A network with fewer than PaidMinSessions such
// receipts has no entry. Pure.
func PaidRates(rs []Receipt, now time.Time, currency string) map[string]PaidRate {
	byNet := map[string][]Receipt{}
	for _, r := range rs {
		if r.TotalCost == nil || r.EnergyKWh == nil || *r.TotalCost <= 0 || *r.EnergyKWh <= 0 {
			continue
		}
		if r.IsHome != nil && *r.IsHome {
			continue
		}
		if !strings.EqualFold(r.Currency, currency) || now.Sub(r.EndedAt) > PaidMaxAge || r.EndedAt.After(now) {
			continue
		}
		n := dcfcrates.MatchNetwork(r.Vendor + " " + r.ChargerName)
		if n.Slug == dcfcrates.UnmatchedNetwork.Slug {
			continue
		}
		byNet[n.Slug] = append(byNet[n.Slug], r)
	}
	out := map[string]PaidRate{}
	for slug, list := range byNet {
		sort.Slice(list, func(i, j int) bool { return list[i].EndedAt.After(list[j].EndedAt) })
		if len(list) > PaidMaxSessions {
			list = list[:PaidMaxSessions]
		}
		if len(list) < PaidMinSessions {
			continue
		}
		var cost, kwh float64
		for _, r := range list {
			cost += *r.TotalCost
			kwh += *r.EnergyKWh
		}
		out[slug] = PaidRate{Slug: slug, Currency: strings.ToUpper(currency), PerKWh: cost / kwh, Sessions: len(list)}
	}
	return out
}

// PaidRates reads the user's receipts and returns their paid $/kWh per network. Empty
// for a user without receipts (they must have opted in for any to exist).
func (s *Store) PaidRates(ctx context.Context, uid uuid.UUID, now time.Time, currency string) (map[string]PaidRate, error) {
	rs, err := s.ListAll(ctx, uid)
	if err != nil {
		return nil, err
	}
	return PaidRates(rs, now, currency), nil
}
