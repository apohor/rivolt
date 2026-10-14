package main

import (
	"context"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// dcfcMatchRadiusM is how far a recorded DC fast charge may sit from an
// NREL station and still be attributed to it: a large lot plus GPS
// error, well short of the next station over.
const dcfcMatchRadiusM = 250.0

// newDCFCPricer prices a DC fast charge Rivian didn't bill at the rate
// of the nearest NREL DCFC station's network, honouring the user's
// memberships (ss may be nil: guest rates). No station within
// dcfcMatchRadiusM, or no archive, falls back to the rate table's
// "Unknown DCFC" row. Shared by the recorder and `rivolt reprice` so
// both price a session the same way.
func newDCFCPricer(archive *chargers.Archive, ss *settings.Store) rivian.DCFCPriceLookup {
	return func(lat, lon float64) (float64, string, string, bool) {
		query := ""
		if archive != nil && (lat != 0 || lon != 0) {
			if poi, ok, err := archive.Nearest(lat, lon, dcfcMatchRadiusM, chargers.FilterDCFC); err == nil && ok {
				query = dcfcrates.StationQuery(poi.EVNetwork, poi.Name)
			}
		}
		var overrides []dcfcrates.NetworkOverride
		if ss != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if nets, err := settings.GetChargingNetworks(ctx, ss); err == nil {
				overrides = settings.AsOverrides(nets)
			}
			cancel()
		}
		rr := dcfcrates.ResolveRate(query, overrides)
		return rr.UserRate, "USD", rr.DisplayName, rr.UserRate > 0
	}
}
