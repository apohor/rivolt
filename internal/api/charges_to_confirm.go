package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/histwin"
	"github.com/apohor/rivolt/internal/settings"
)

// maxToConfirmRows bounds GET /api/charges/to-confirm.
const maxToConfirmRows = 500

// toConfirmCharge is one row of GET /api/charges/to-confirm: exactly what the iPhone's
// Money "To confirm" list reads. The first five keys are the ones /api/charges uses for
// the same fields (the app decodes them unchanged); network_state and network_evidence
// are its snake-case twins. No coordinates, place or address.
type toConfirmCharge struct {
	ID              string               `json:"ID"`
	StartedAt       time.Time            `json:"StartedAt"`
	PricePerKWh     float64              `json:"PricePerKWh"`
	Cost            float64              `json:"Cost"`
	Currency        string               `json:"Currency"`
	NetworkState    string               `json:"network_state"`
	NetworkEvidence toConfirmEvidenceOut `json:"network_evidence"`
}

type toConfirmEvidenceOut struct {
	Candidates []toConfirmCandidateOut `json:"candidates"`
}

type toConfirmCandidateOut struct {
	Network string `json:"network"`
}

// toConfirmEvidenceIn reads only the network name of each candidate out of the stored
// evidence, which holds more (distances, slugs, what the detection saw).
type toConfirmEvidenceIn struct {
	Candidates []struct {
		Network string `json:"network"`
	} `json:"candidates"`
}

// isOpenNetworkState is the app's isOpenNetworkState and the summary's to-confirm count:
// an auto-detected network the owner has not confirmed yet.
func isOpenNetworkState(s string) bool { return s == "estimated" || s == "needs_confirmation" }

// handleChargesToConfirm: GET /api/charges/to-confirm lists the caller's charges whose
// auto-detected network still waits for a look (estimated or needs_confirmation),
// newest first, at most maxToConfirmRows, coordinate-free, so the phone need not pull
// the whole charge list for it. The rows go through the same decoration as
// /api/charges (billed receipts clear a matched charge's state), from the latest 5000.
func handleChargesToConfirm(store *charges.Store, settingsStore *settings.Store, billed func(context.Context) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out := []toConfirmCharge{}
		if store == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		rows, err := listChargesFor(r.Context(), store, 5000)
		if err != nil {
			http.Error(w, "could not read charges", http.StatusInternalServerError)
			return
		}
		for _, c := range decorateCharges(r.Context(), rows, settingsStore, billed != nil && billed(r.Context())) {
			if c.NetworkStateOut == nil || !isOpenNetworkState(*c.NetworkStateOut) || !histwin.Allows(r.Context(), c.StartedAt) {
				continue
			}
			if len(out) >= maxToConfirmRows {
				break
			}
			row := toConfirmCharge{
				ID: c.ID, StartedAt: c.StartedAt, PricePerKWh: c.PricePerKWh, Cost: c.Cost, Currency: c.Currency,
				NetworkState:    *c.NetworkStateOut,
				NetworkEvidence: toConfirmEvidenceOut{Candidates: []toConfirmCandidateOut{}},
			}
			var ev toConfirmEvidenceIn
			if len(c.NetworkEvidenceOut) > 0 && json.Unmarshal(c.NetworkEvidenceOut, &ev) == nil {
				for _, cand := range ev.Candidates {
					row.NetworkEvidence.Candidates = append(row.NetworkEvidence.Candidates, toConfirmCandidateOut{Network: cand.Network})
				}
			}
			out = append(out, row)
		}
		writeJSON(w, http.StatusOK, out)
	}
}
