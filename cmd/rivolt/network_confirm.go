package main

import (
	"encoding/json"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/push"
)

// networkConfirmFor maps a just-closed charge to the confirm-or-change
// push, or false when the charge carries no open auto-detection state
// (AC, Rivian-billed, user-priced, legacy, or detection off).
func networkConfirmFor(c charges.Charge) (push.NetworkConfirm, bool) {
	if c.NetworkState != "estimated" && c.NetworkState != "needs_confirmation" {
		return push.NetworkConfirm{}, false
	}
	e := push.NetworkConfirm{
		ChargeID: c.ID,
		Network:  c.Network,
		Cost:     c.Cost,
		Rate:     c.PricePerKWh,
		KWh:      c.EnergyAddedKWh,
		State:    c.NetworkState,
		// Nothing matched: no estimate was written.
		Matched: c.CostSource == "network_estimate" && c.Network != "",
	}
	var ev struct {
		Candidates []struct {
			Network string `json:"network"`
		} `json:"candidates"`
	}
	if len(c.NetworkEvidence) > 0 && json.Unmarshal(c.NetworkEvidence, &ev) == nil {
		names := make([]string, 0, len(ev.Candidates))
		for _, cand := range ev.Candidates {
			names = append(names, cand.Network)
		}
		e.Other = push.CandidateOther(c.Network, names)
	}
	return e, true
}
