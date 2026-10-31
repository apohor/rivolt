package api

import (
	"testing"

	"github.com/apohor/rivolt/internal/db"
)

func TestVehicleLabel(t *testing.T) {
	vs := []db.VehicleSummary{
		{RivianVehicleID: "rv-test-1", Model: "R1T", DisplayName: "Big Blue"},
		{RivianVehicleID: "rv-test-2", Model: "R1S", DisplayName: "Family"},
		{RivianVehicleID: "rv-test-3", Model: "R1S", DisplayName: "Spare"},
		{RivianVehicleID: "rv-test-4", Model: "", DisplayName: "Old one"},
		{RivianVehicleID: "rv-test-5"},
	}
	for id, want := range map[string]string{
		"rv-test-1": "R1T",    // one of its model: the model, not the nickname
		"rv-test-2": "Family", // two R1S: the nickname tells them apart
		"rv-test-3": "Spare",
		"rv-test-4": "Old one", // no model at all
		"rv-test-5": "your primary vehicle",
		"rv-test-9": "your primary vehicle",
	} {
		if got := vehicleLabel(vs, id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

// Without an unsubscribe signer the statement job does not start (every email carries a signed link): it returns at once, no goroutine,
// no panic mid-send.
func TestStatementJobNeedsASigner(t *testing.T) {
	startStatementJob(Deps{}, nil)
}
