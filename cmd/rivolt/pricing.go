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
// memberships (ss may be nil: guest rates). ok=false when no station is
// within dcfcMatchRadiusM or the archive isn't loaded: the session
// stays unpriced rather than getting a guessed rate stamped on it.
// Shared by the recorder and `rivolt reprice` so both price alike.
func newDCFCPricer(archive *chargers.Archive, ss *settings.Store) rivian.DCFCPriceLookup {
	return func(lat, lon float64) (float64, string, string, bool) {
		if archive == nil || (lat == 0 && lon == 0) {
			return 0, "", "", false
		}
		poi, ok, err := archive.Nearest(lat, lon, dcfcMatchRadiusM, chargers.FilterDCFC)
		if err != nil || !ok {
			return 0, "", "", false
		}
		query := dcfcrates.StationQuery(poi.EVNetwork, poi.Name)
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
