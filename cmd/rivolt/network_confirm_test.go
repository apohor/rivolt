package main

import (
	"encoding/json"
	"testing"

	"github.com/apohor/rivolt/internal/charges"
)

func TestNetworkConfirmFor(t *testing.T) {
	if _, ok := networkConfirmFor(charges.Charge{ID: "a", CostSource: "rivian"}); ok {
		t.Error("rivian-billed charge must not prompt")
	}
	if _, ok := networkConfirmFor(charges.Charge{ID: "a", NetworkState: "confirmed"}); ok {
		t.Error("confirmed charge must not prompt")
	}
	e, ok := networkConfirmFor(charges.Charge{
		ID: "c", CostSource: "network_estimate", Network: "Tesla Supercharger", Cost: 16, PricePerKWh: 0.4,
		NetworkState:    "needs_confirmation",
		NetworkEvidence: json.RawMessage(`{"candidates":[{"network":"Tesla Supercharger"},{"network":"Rivian Adventure Network"}]}`),
	})
	if !ok || !e.Matched || e.Other != "Rivian Adventure Network" {
		t.Errorf("colocated = %+v ok=%v", e, ok)
	}
	e, ok = networkConfirmFor(charges.Charge{ID: "u", NetworkState: "needs_confirmation", EnergyAddedKWh: 30})
	if !ok || e.Matched {
		t.Errorf("unmatched = %+v ok=%v", e, ok)
	}
}
