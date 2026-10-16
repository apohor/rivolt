package main

import (
	"context"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/charges"
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

// newDCFCDetector detects and prices the network of a DC fast charge
// Rivian didn't bill (contract dcfc-auto-network v1): Rivian's
// isRivianCharger, then the user's own verified charge at the site
// (store, may be nil), then every archive DC station within the site
// radius, priced at the user's rate with their memberships (ss may be
// nil). enabled says whether the dcfc_auto_network flag covers this
// user; when it doesn't the detector reports off and the recorder keeps
// the plain pricer.
func newDCFCDetector(archive *chargers.Archive, ss *settings.Store, store *charges.Store, enabled func() bool) rivian.DCFCDetector {
	return func(lat, lon float64, rivianCharger bool, peakKW float64) (rivian.DCFCVerdict, bool) {
		if enabled == nil || !enabled() {
			return rivian.DCFCVerdict{}, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		in := dcfcrates.DetectInput{RivianCharger: rivianCharger, PeakKW: peakKW}
		if lat != 0 || lon != 0 {
			if store != nil {
				if vn, ok, err := store.NearestVerifiedNetwork(ctx, lat, lon, dcfcrates.SiteMemoryRadiusM); err == nil && ok {
					in.Site = &dcfcrates.SiteMemory{Network: vn.Network, DistM: vn.DistM}
				}
			}
			if archive != nil {
				if near, err := archive.Within(lat, lon, dcfcrates.SiteRadiusM, chargers.FilterDCFC); err == nil {
					for _, p := range near {
						in.Candidates = append(in.Candidates, dcfcrates.Candidate{
							Name: p.Name, Network: p.EVNetwork, DistM: p.DistM, MaxPowerKW: p.MaxPowerKW,
						})
					}
				}
			}
		}
		d := dcfcrates.Detect(in, "open")
		v := rivian.DCFCVerdict{Network: d.Network, State: d.State, Confidence: d.Confidence, Evidence: d.Evidence}
		if d.Query == "" {
			return v, true
		}
		var overrides []dcfcrates.NetworkOverride
		if ss != nil {
			if nets, err := settings.GetChargingNetworks(ctx, ss); err == nil {
				overrides = settings.AsOverrides(nets)
			}
		}
		rr := dcfcrates.ResolveRate(d.Query, overrides)
		if rr.UserRate > 0 {
			v.Priced, v.Rate, v.Currency, v.Network = true, rr.UserRate, "USD", rr.DisplayName
		}
		return v, true
	}
}
