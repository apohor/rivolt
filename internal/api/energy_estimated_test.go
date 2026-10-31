package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

// energy_estimated rides on every charge the API and the export serve, false unless the energy was estimated from the SoC
// gain (its metered count had been lost); the cost is estimated with it.
func TestEnergyEstimatedIsServed(t *testing.T) {
	// The activity feed: always present, false by default.
	raw, err := json.Marshal(activityCharge{ID: "c"})
	if err != nil || !strings.Contains(string(raw), `"energy_estimated":false`) {
		t.Errorf("activity charge: %s %v, want the key present and false", raw, err)
	}
	raw, _ = json.Marshal(activityCharge{ID: "c", EnergyEstimated: true})
	if !strings.Contains(string(raw), `"energy_estimated":true`) {
		t.Errorf("activity charge: %s, want true", raw)
	}
	// The part credited across telemetry gaps rides along when there is one, and is absent when not.
	raw, _ = json.Marshal(activityCharge{ID: "c", EnergyEstimated: true, GapKWh: 4.5})
	if !strings.Contains(string(raw), `"gap_kwh":4.5`) {
		t.Errorf("activity charge: %s, want gap_kwh 4.5", raw)
	}
	if raw, _ = json.Marshal(activityCharge{ID: "c"}); strings.Contains(string(raw), "gap_kwh") {
		t.Errorf("activity charge without a gap: %s, want no gap_kwh", raw)
	}

	// The CSV export: a last column, in step with the header, true only for the estimated row.
	e := sampleExport()
	e.Charges[0].EnergyEstimated = true
	var buf bytes.Buffer
	if err := writeChargesCSV(&buf, e); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("csv: %v %v", recs, err)
	}
	last := len(exportChargeHeader) - 1
	if exportChargeHeader[last] != "energy_estimated" || len(recs[1]) != len(exportChargeHeader) || recs[1][last] != "true" {
		t.Errorf("csv last column %q = %q (row has %d fields, header %d), want energy_estimated = true", exportChargeHeader[last], recs[1][last], len(recs[1]), len(exportChargeHeader))
	}
	e.Charges[0].EnergyEstimated = false
	buf.Reset()
	_ = writeChargesCSV(&buf, e)
	recs, _ = csv.NewReader(&buf).ReadAll()
	if recs[1][last] != "false" {
		t.Errorf("a metered charge exports %q", recs[1][last])
	}

	// The charge list and detail serialize the stored Charge, so the field is on it, false by default.
	raw, _ = json.Marshal(chargeResponse{})
	if !strings.Contains(string(raw), `"EnergyEstimated":false`) {
		t.Errorf("charge response lacks EnergyEstimated: %s", raw)
	}
}
