package dcfcrates

import (
	"fmt"
	"time"
)

// Observed prices: the "typical price" a third-party tracker has seen at a network's
// chargers. None of the networks publishes one national per-kWh price (each site sets
// its own and shows it in the app or at the charger), so this is NOT a listed price and
// is never presented as one. It is its own table, with its source, link and observation
// month, because the rules for it differ from the user's own figures (DA, 2026-10-20):
//
//   - the label is exactly Label(): "Typical price seen by DCFC Tracker, {Mon YYYY}. The
//     price at the charger may differ."; never "listed", "official" or "current";
//   - never merged into the user's own receipts figure and never pre-filled as the
//     user's rate;
//   - hidden once the observation is over 12 months old (ObservedFor), after which the
//     planner says the price is shown at the charger;
//   - copied BY HAND at each re-check (their terms forbid scraping), one figure per row.
//
// Low and High are the same when the network has one observed average. They differ when
// the tracker reports two (a membership price and a non-member price): Low is the
// lower, High the higher, so a trip total can be shown as a range.

// ObservedSource is the tracker's name, shown with every figure.
const ObservedSource = "DCFC Tracker"

// ObservedPrice is one network's hand-copied observation.
type ObservedPrice struct {
	Slug string
	// Low and High are USD per kWh (see the note above).
	Low, High float64
	// Month is the observation month, "2006-01" form (the month of the tracker report).
	Month string
	// URL is the tracker page the figure was copied from.
	URL string
	// Note says what Low and High are, for whoever re-checks the row.
	Note string
}

// observedPrices are the rows (checked by hand 2026-10-20; growth's recheck, scratchpad
// dcfc-rates-recheck.md). Networks with no figure are absent: the planner says the
// price is shown at the charger.
var observedPrices = []ObservedPrice{
	{Slug: "ran", Low: 0.482, High: 0.482, Month: "2026-10", URL: "https://dcfctracker.com/reports/rivian",
		Note: "average for Rivian vehicles, where the owner discount is applied automatically (standard average 0.550)"},
	{Slug: "tesla_sc", Low: 0.434, High: 0.624, Month: "2026-10", URL: "https://dcfctracker.com/reports/supercharger",
		Note: "non-Tesla vehicles: 0.624 without, 0.434 with the Supercharging Membership"},
	{Slug: "evgo", Low: 0.636, High: 0.636, Month: "2026-10", URL: "https://dcfctracker.com/reports/evgo",
		Note: "network average"},
	{Slug: "ea", Low: 0.613, High: 0.613, Month: "2026-10", URL: "https://dcfctracker.com/reports/electrify-america",
		Note: "network average"},
}

// observedMaxAge is how old an observation may be and still be shown.
const observedMaxAge = 12 // months

// ObservedFor returns the observed price for a network slug when there is one and it is
// not over 12 months old at now. The month counts from its first day, so a figure
// observed in 2026-10 stops showing on 2027-10-01: earlier than the day it was copied,
// never later.
func ObservedFor(slug string, now time.Time) (ObservedPrice, bool) {
	for _, o := range observedPrices {
		if o.Slug != slug {
			continue
		}
		m, err := time.Parse("2006-01", o.Month)
		if err != nil || !now.Before(m.AddDate(0, observedMaxAge, 0)) {
			return ObservedPrice{}, false
		}
		return o, true
	}
	return ObservedPrice{}, false
}

// Label is the exact wording for the figure.
func (o ObservedPrice) Label() string {
	m, err := time.Parse("2006-01", o.Month)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("Typical price seen by %s, %s. The price at the charger may differ.", ObservedSource, m.Format("Jan 2006"))
}
