package api

import (
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// observedTermsURL is the tracker's terms page; the charging-networks page links it
// beside every figure it copied (checked 2026-10-20: no scraping or bulk download, no
// republishing prohibition; figures are copied by hand, one at a time).
const observedTermsURL = "https://dcfctracker.com/terms"

// priceAtCharger is what a network without a (still shown) observed figure says.
const priceAtCharger = "Price shown at the charger."

type observedJSON struct {
	LowPerKWh  float64 `json:"low_per_kwh"`
	HighPerKWh float64 `json:"high_per_kwh"`
	LowNote    string  `json:"low_note,omitempty"`
	HighNote   string  `json:"high_note,omitempty"`
	Month      string  `json:"month"`  // YYYY-MM of the tracker's report
	Source     string  `json:"source"` // "DCFC Tracker"
	URL        string  `json:"url"`    // the tracker page the figure was copied from
	Label      string  `json:"label"`  // "Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ."
}

type chargingNetworkJSON struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Plans are the membership terms as the operator states them (rows older than 12
	// months are left out). Empty when the network states none.
	Plans []membershipTermJSON `json:"plans"`
	// Observed is the third-party typical price, or null once it is over 12 months old
	// or when there never was one; then Price says "Price shown at the charger.".
	Observed *observedJSON `json:"observed"`
	// Price is the one line for the price column: the observed label, or priceAtCharger.
	Price string `json:"price"`
}

// handleChargingNetworks serves GET /api/charging-networks: the public reference data behind
// the charging-networks comparison page. The same for everyone and read from nothing
// about the caller. No network publishes one national price per kWh, so Price is never a
// listed price: it is the tracker's typical observed price with its label and month, or
// "Price shown at the charger.". A figure over 12 months old is not returned (the server
// rule, dcfcrates.ObservedFor), and neither is a plan last checked over 12 months ago.
func handleChargingNetworks(now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		at := now()
		plans := map[string][]membershipTermJSON{}
		for _, t := range dcfcrates.MembershipTermsAt(at) {
			plans[t.Slug] = append(plans[t.Slug], membershipTermJSON{
				NetworkSlug: t.Slug, Network: t.Network, Plan: t.Plan, Terms: t.Terms,
				SourceURL: t.SourceURL, ReadVia: t.ReadVia, Checked: t.Checked, Label: t.Label,
			})
		}
		out := []chargingNetworkJSON{}
		for _, n := range dcfcrates.Networks {
			o, hasObs := dcfcrates.ObservedFor(n.Slug, at)
			if len(plans[n.Slug]) == 0 && !hasObs {
				continue
			}
			c := chargingNetworkJSON{Slug: n.Slug, Name: n.DisplayName, Plans: plans[n.Slug], Price: priceAtCharger}
			if c.Plans == nil {
				c.Plans = []membershipTermJSON{}
			}
			if hasObs {
				c.Observed = &observedJSON{
					LowPerKWh: o.Low, HighPerKWh: o.High, LowNote: o.LowNote, HighNote: o.HighNote,
					Month: o.Month, Source: dcfcrates.ObservedSource, URL: o.URL, Label: o.Label(),
				}
				c.Price = o.Label()
			}
			out = append(out, c)
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		writeJSON(w, http.StatusOK, map[string]any{
			"networks":           out,
			"observed_terms_url": observedTermsURL,
			"observed_source":    dcfcrates.ObservedSource,
		})
	}
}
