package rivian

import (
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/dcfcrates"
)

// PriceSettings is what a charge is priced with: the monitor's per-user lookups, copied under its lock. Any may be
// nil. The lookups are database or archive reads; none of them calls Rivian.
type PriceSettings struct {
	Home     PriceLookup     // the user's home $/kWh rate
	DCFC     DCFCPriceLookup // station rate for a fast charge
	Detect   DCFCDetector    // network detection with a verdict, where it is on for the user
	Estimate DCFCEstimator   // typical public price when the user has no rate
	HomeLoc  HomeLookup      // the user's saved home location
}

// NetworkMemo is the per-charge network pricing state the recorder keeps between the builds of an open session: the
// cached station rate (so per-frame upserts do not re-run the lookup) and the detection verdict, with the peak it was
// made at (a co-located site is looked at again when the peak has grown, since a peak only one network could deliver
// can settle it). The zero value is right for a charge priced once, as a repair does.
type NetworkMemo struct {
	Priced  bool
	Rate    float64
	Cur     string
	Network string
	// Verdict is network detection's verdict once made.
	Verdict       *DCFCVerdict
	PeakKW        float64
	RivianCharger bool // the verdict saw isRivianCharger
}

// PriceUnbilled prices a charge Rivian did not bill: a fast charge at the network rate of the station it happened at
// (or the detected network's), anything slower at the user's home rate, and AC away from home left unpriced. It fills
// Cost, PricePerKWh, Currency, CostSource, Network and the network_* fields on row.
//
// Preconditions: row.Cost == 0 and row.EnergyAddedKWh > 0. It reads StartedAt, EndedAt, Lat, Lon, MaxPowerKW,
// ActiveSeconds, EnergyAddedKWh and RivianCharger, and updates memo. It makes no Rivian calls.
func PriceUnbilled(row *charges.Charge, s PriceSettings, memo *NetworkMemo) {
	energy := row.EnergyAddedKWh
	lookup, dcfc, home, detect, estimate := s.Home, s.DCFC, s.HomeLoc, s.Detect, s.Estimate
	fast := (dcfc != nil || detect != nil) && IsDCFCSession(row.MaxPowerKW, energy, row.ActiveSeconds, row.EndedAt.Sub(row.StartedAt))
	// Network detection with a verify state, where it's on for the
	// user. Rivian's isRivianCharger (this charge's own session,
	// see sessionPredatesCharge) is its strongest signal, so a
	// verdict made without it is made again once it arrives.
	ranCharger := row.RivianCharger != nil && *row.RivianCharger
	if fast && detect != nil && (memo.Verdict == nil ||
		(ranCharger && !memo.RivianCharger) ||
		(memo.Verdict.Confidence < dcfcrates.ConfidentAt && row.MaxPowerKW > memo.PeakKW*1.1)) {
		if v, on := detect(row.Lat, row.Lon, ranCharger, row.MaxPowerKW); on {
			memo.Verdict, memo.PeakKW, memo.RivianCharger = &v, row.MaxPowerKW, ranCharger
		}
	}
	if fast && memo.Verdict != nil {
		v := memo.Verdict
		row.NetworkState, row.NetworkEvidence = v.State, v.Evidence
		conf := v.Confidence
		row.NetworkConfidence = &conf
		if v.Priced && v.Rate > 0 {
			row.PricePerKWh = v.Rate
			row.Currency = v.Currency
			row.Cost = v.Rate * energy
			row.CostSource = CostSourceNetworkEstimate
			row.Network = v.Network
		}
		// No rate of the user's: a typical public price for the
		// detected network, when there is one for the session date.
		if row.CostSource != CostSourceNetworkEstimate && v.Slug != "" && estimate != nil {
			applyDCFCEstimate(row, energy, estimate, v.Slug)
		}
		// Still unpriced (nothing matched, no typical price): the row
		// waits for the user, network_state says so.
	} else if fast && dcfc != nil {
		if !memo.Priced {
			if rate, cur, network, ok := dcfc(row.Lat, row.Lon); ok && rate > 0 {
				memo.Priced, memo.Rate, memo.Cur, memo.Network = true, rate, cur, network
			}
		}
		if memo.Priced {
			row.PricePerKWh = memo.Rate
			row.Currency = memo.Cur
			row.Cost = memo.Rate * energy
			row.CostSource = CostSourceNetworkEstimate
			row.Network = memo.Network
		}
		if !memo.Priced && estimate != nil {
			applyDCFCEstimate(row, energy, estimate, "")
		}
		// No station matched (or the archive isn't loaded): leave
		// it unpriced rather than at the home rate or a guessed
		// network. The ledger shows "unknown", and `rivolt reprice`
		// can price it later; nothing is stamped.
	}
	// AC charging away from home (a destination L1/L2, a friend's
	// outlet, a paid public L2) has no known price: leave it unpriced
	// for the user to fill in rather than stamp the home rate on it.
	// Without a saved home location or a session position we can't
	// tell, so slow charging keeps the home rate.
	if !fast && row.Cost == 0 && lookup != nil && !AwayFromHome(row.Lat, row.Lon, home) {
		if rate, cur := lookup(); rate > 0 {
			row.PricePerKWh = rate
			row.Currency = cur
			row.Cost = rate * energy
			row.CostSource = CostSourceHomeRate
		}
	}
}
