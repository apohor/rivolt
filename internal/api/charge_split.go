package api

import (
	"context"
	"net/http"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/settings"
)

// splitBucket is one slice of the Home / Away / Fast charge split.
type splitBucket struct {
	Sessions  int     `json:"sessions"`
	EnergyKWh float64 `json:"energy_kwh"`
}

// chargeSplit is the answer of GET /api/charge-split: how many charges, and how many
// kWh, were home AC, away AC and DC fast in the window. total counts the labelled
// charges only (an unlabelled one is in no bucket and not in total), so it is the
// denominator the shares use.
type chargeSplit struct {
	Home  splitBucket `json:"home"`
	Away  splitBucket `json:"away"`
	Fast  splitBucket `json:"fast"`
	Total int         `json:"total"`
}

// Place labels, as the web's chargePlaceLabel returns them.
const (
	placeHome = "Home"
	placeAway = "Away"
	placeFast = "Fast"
)

// chargePlaceLabel is the web's chargePlaceLabel (lib/chargePlace.ts): the server's
// classification first (a DC fast charge is Fast wherever it was; an AC charge is Home
// or Away by place), the cluster label only where the classification cannot say.
// "" means no label.
func chargePlaceLabel(kind, place string, cluster analytics.ClusterLabel) string {
	if kind == "dcfc" {
		return placeFast
	}
	if kind == "ac" {
		switch place {
		case "home":
			return placeHome
		case "away":
			return placeAway
		}
		// The kind is known, so the cluster may only settle the place.
		switch cluster {
		case analytics.LabelHome:
			return placeHome
		case analytics.LabelPublic:
			return placeAway
		}
		return ""
	}
	switch cluster {
	case analytics.LabelHome:
		return placeHome
	case analytics.LabelPublic:
		return placeAway
	case analytics.LabelFast:
		return placeFast
	}
	return ""
}

// clusterLabels labels every charge by the same clustering GET /api/charges/clusters
// serves (the latest 5000 charges, all trucks), by charge id.
func clusterLabels(rows []charges.Charge) map[string]analytics.ClusterLabel {
	pts := make([]analytics.ChargePoint, 0, len(rows))
	for _, c := range rows {
		pts = append(pts, analytics.ChargePoint{ID: c.ID, Lat: c.Lat, Lon: c.Lon, EnergyAddedKWh: c.EnergyAddedKWh, MaxPowerKW: c.MaxPowerKW})
	}
	out := map[string]analytics.ClusterLabel{}
	for _, cl := range analytics.ClusterCharges(pts, analytics.DefaultParams()) {
		for _, id := range cl.MemberIDs {
			out[id] = cl.Label
		}
	}
	return out
}

// handleChargeSplit: GET /api/charge-split?since&until&vehicle_id (the same window
// and truck rules as /api/chart-points). It labels the user's charges exactly as the
// web does: the ledger's classification of the full list (so a charge is classified
// the same here as on Money), the cluster label as the fallback, and counts the ones
// started in the window, optionally one truck. The same row rules as the list (a
// minute or more). The answer is counts and kWh only: no coordinates, ids, costs or
// places.
func handleChargeSplit(store *charges.Store, settingsStore *settings.Store, billed func(context.Context) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		since, until, vehicleID, ok := chartWindow(w, r)
		if !ok {
			return
		}
		var out chargeSplit
		if store == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		// The web labels the window's charges from the latest 5000 (classification
		// and clusters both), so this does too, then narrows to the window and truck.
		rows, err := store.ListRecent(r.Context(), 5000)
		if err != nil {
			http.Error(w, "could not read charges", http.StatusInternalServerError)
			return
		}
		clusters := clusterLabels(rows)
		for _, c := range decorateCharges(r.Context(), rows, settingsStore, billed != nil && billed(r.Context())) {
			if c.StartedAt.Before(since) || !c.StartedAt.Before(until) || (vehicleID != "" && c.VehicleID != vehicleID) {
				continue
			}
			var kind, place string
			if c.Classification != nil {
				kind, place = c.Classification.Kind, c.Classification.Place
			}
			var b *splitBucket
			switch chargePlaceLabel(kind, place, clusters[c.ID]) {
			case placeHome:
				b = &out.Home
			case placeAway:
				b = &out.Away
			case placeFast:
				b = &out.Fast
			default:
				continue
			}
			b.Sessions++
			b.EnergyKWh += c.EnergyAddedKWh
			out.Total++
		}
		writeJSON(w, http.StatusOK, out)
	}
}
