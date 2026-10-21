package ledger

import (
	"encoding/json"
	"testing"
)

// A fast charge priced from a typical public price is in the total and the
// priced count, but is reported on its own lines and never blended into the
// user's own figures.
func TestObservedEstimateIsPricedAndReportedSeparately(t *testing.T) {
	est := priced(at(charge("est", "2026-10-21T14:00:00Z", 40*60e9, 50, 150), awayLat, awayLon), 0.636)
	est.CostSource, est.Network = SourceObservedEstimate, "EVgo"
	est.EstimateMonth, est.EstimateURL = "2026-10", "https://dcfctracker.com/reports/evgo"
	own := priced(at(charge("own", "2026-10-24T14:00:00Z", 40*60e9, 40, 150), awayLat+1, awayLon), 0.50)
	own.CostSource = SourceNetworkEstimate
	none := at(charge("none", "2026-10-27T14:00:00Z", 40*60e9, 30, 150), awayLat+2, awayLon)
	ac := at(charge("ac", "2026-10-30T14:00:00Z", 2*3600e9, 20, 11), awayLat+3, awayLon)

	p, err := ParsePeriod("2026-10", chicago)
	if err != nil {
		t.Fatal(err)
	}
	m := Build(Input{Charges: []Charge{est, own, none, ac}, Config: cfg(), Now: ts("2026-11-01T00:00:00Z")}, p, true)

	if m.Spend.ObservedEstimateSessions != 1 || m.Spend.ObservedEstimateCost != round4(0.636*50) {
		t.Errorf("observed estimate: %d sessions $%v, want 1 and %v", m.Spend.ObservedEstimateSessions, m.Spend.ObservedEstimateCost, round4(0.636*50))
	}
	if m.Spend.BySource[SourceObservedEstimate] != 0.636*50 || m.Spend.BySource[SourceNetworkEstimate] != 0.50*40 {
		t.Errorf("by_source %v: the estimate must not land in the user's network line", m.Spend.BySource)
	}
	if want := round4(0.636*50 + 0.50*40); m.Spend.Total != want || m.Spend.PricedSessions != 2 {
		t.Errorf("total %v priced %d, want %v and 2 (the estimate counts)", m.Spend.Total, m.Spend.PricedSessions, want)
	}
	if m.Spend.UnpricedSessions != 2 || m.Spend.UnpricedFastSessions != 1 {
		t.Errorf("unpriced %d fast %d, want 2 and 1 (the fast one is the only one a rate prices)", m.Spend.UnpricedSessions, m.Spend.UnpricedFastSessions)
	}

	var gotEst, gotNone *Receipt
	for i := range m.Receipts {
		switch m.Receipts[i].ReceiptID {
		case "rcpt_est":
			gotEst = &m.Receipts[i]
		case "rcpt_none":
			gotNone = &m.Receipts[i]
		}
	}
	if gotEst == nil || gotNone == nil {
		t.Fatalf("receipts: %+v", m.Receipts)
	}
	if gotEst.CostSource != SourceObservedEstimate || gotEst.EstimateMonth != "2026-10" || gotEst.EstimateURL != "https://dcfctracker.com/reports/evgo" || gotEst.NeedsRate {
		t.Errorf("estimate receipt: %+v", gotEst)
	}
	if gotNone.CostSource != SourceUnknown || !gotNone.NeedsRate || gotNone.EstimateMonth != "" {
		t.Errorf("unpriced fast receipt: %+v", gotNone)
	}

	cl := Classify(est, cfg())
	if cl.CostSource != SourceObservedEstimate || !cl.CountsInSpend || cl.EstimateMonth != "2026-10" || cl.NeedsRate {
		t.Errorf("classify estimate: %+v", cl)
	}
	if cl := Classify(none, cfg()); !cl.NeedsRate || cl.CostSource != SourceUnknown {
		t.Errorf("classify unpriced fast: %+v", cl)
	}
	if cl := Classify(ac, cfg()); cl.NeedsRate {
		t.Errorf("an unpriced AC session is not asking for a fast-charging rate: %+v", cl)
	}
}

// Rivian's bill replaces the estimate: the session becomes rivian, in the
// billed bucket, with no estimate fields.
func TestObservedEstimateReplacedByReceipt(t *testing.T) {
	est := priced(at(charge("est", "2026-10-21T14:00:00Z", 40*60e9, 50, 150), awayLat, awayLon), 0.636)
	est.CostSource = SourceObservedEstimate
	est.EstimateMonth, est.EstimateURL = "2026-10", "https://dcfctracker.com/reports/evgo"
	bill := 29.0
	est.BilledCost, est.BilledCurrency = &bill, "USD"
	c := cfg()
	c.Billed = true

	p, _ := ParsePeriod("2026-10", chicago)
	m := Build(Input{Charges: []Charge{est}, Config: c, Now: ts("2026-11-01T00:00:00Z")}, p, true)
	if m.Spend.ObservedEstimateSessions != 0 || m.Spend.ObservedEstimateCost != 0 || m.Spend.Total != 29 {
		t.Errorf("after the bill: %+v", m.Spend)
	}
	if r := m.Receipts[0]; r.CostSource != SourceRivian || r.EstimateMonth != "" || r.EstimateURL != "" {
		t.Errorf("receipt after the bill: %+v", r)
	}
	if cl := Classify(est, c); cl.CostSource != SourceRivian || cl.EstimateMonth != "" {
		t.Errorf("list classification after the bill: %+v", cl)
	}
}

// The unpriced fast session names the network the detection saw, so the app
// can say "Set your rate for EVgo".
func TestNeedsRateCarriesTheDetectedNetwork(t *testing.T) {
	c := at(charge("n", "2026-10-27T14:00:00Z", 40*60e9, 30, 150), awayLat, awayLon)
	c.NetworkEvidence = json.RawMessage(`{"v":1,"candidates":[{"network":"EVgo","slug":"evgo","dist_m":40},{"network":"Tesla","slug":"tesla_sc","dist_m":90}]}`)
	if cl := Classify(c, cfg()); !cl.NeedsRate || cl.RateSlug != "evgo" {
		t.Errorf("classify: %+v", cl)
	}
	c.NetworkEvidence = nil
	if cl := Classify(c, cfg()); !cl.NeedsRate || cl.RateSlug != "" {
		t.Errorf("no detection: %+v", cl)
	}
}

// A month or link the store would never have written (a row inserted around
// it) is not shown.
func TestEstimateFieldsAreCleanedOnRead(t *testing.T) {
	c := priced(at(charge("bad", "2026-10-21T14:00:00Z", 40*60e9, 50, 150), awayLat, awayLon), 0.636)
	c.CostSource = SourceObservedEstimate
	for _, month := range []string{"2026-13", "2026-00", "soon"} {
		c.EstimateMonth, c.EstimateURL = month, "javascript:alert(1)"
		if cl := Classify(c, cfg()); cl.EstimateMonth != "" || cl.EstimateURL != "" || cl.CostSource != SourceObservedEstimate {
			t.Errorf("month %q: %+v, want no month or link but still the estimate", month, cl)
		}
	}
	c.EstimateMonth, c.EstimateURL = "2026-10", "https://dcfctracker.com/reports/evgo"
	if cl := Classify(c, cfg()); cl.EstimateMonth != "2026-10" || cl.EstimateURL == "" {
		t.Errorf("valid values dropped: %+v", cl)
	}
}
