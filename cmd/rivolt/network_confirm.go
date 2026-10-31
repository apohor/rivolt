package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/engage"
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

// networkConfirmKind is the engagement kind that makes the confirm-or-change push once per charge across every replica: a charge's close
// hook can run on two pods around a handover, and a push is not taken back. It is the transactional push that replaces "charging
// complete", so it is claimed as a utility (outside the budget and quiet hours) and only deduplicated.
const networkConfirmKind = "network_confirm"

// claimNetworkConfirm reports whether this pod may send charge chargeID's confirm push, and returns the function that records how
// the send went. A claim that cannot be made (a database fault) still allows the send, as before: a duplicate is better than none.
func claimNetworkConfirm(ctx context.Context, claims engage.Store, uid uuid.UUID, chargeID string, now time.Time) (bool, func(status string)) {
	noop := func(string) {}
	if claims == nil || chargeID == "" {
		return true, noop
	}
	msg := engage.Msg{User: uid, Kind: networkConfirmKind, Key: chargeID, Channel: engage.Push, Utility: true}
	claimed, _, err := claims.Claim(ctx, msg, now, now.Add(-engage.BudgetWindow), engage.MonthlyBudget)
	if err != nil {
		return true, noop
	}
	if !claimed {
		return false, noop
	}
	return true, func(status string) { _ = claims.Mark(ctx, uid, networkConfirmKind, chargeID, status, time.Now()) }
}
