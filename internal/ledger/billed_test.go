package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func billed(c Charge, amount float64, cur, vendor, charger string, home bool) Charge {
	c.BilledCost, c.BilledCurrency, c.BilledVendor, c.BilledCharger, c.BilledHome = f(amount), cur, vendor, charger, home
	return c
}

func billedCfg() Config {
	c := cfg()
	c.Billed = true
	return c
}

// With the rivian_receipts flag off, none of the billed_* keys exist at any
// level, even if the rows carry a receipt.
func TestBilledFieldsAbsentWhenFlagOff(t *testing.T) {
	month, _ := ParsePeriod("2026-10", chicago)
	c := billed(at(priced(charge("a", "2026-10-09T18:00:00Z", time.Hour, 40, 150), 0.40), awayLat, awayLon), 14.25, "USD", "Electrify America", "EA 1234", false)
	m := Build(Input{Charges: []Charge{c}, Config: cfg(), Now: ts("2026-10-13T23:00:00Z")}, month, true)
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "billed_") || strings.Contains(string(raw), "estimated_total") || strings.Contains(string(raw), "estimated_sessions") {
		t.Errorf("billed fields leaked with the flag off: %s", raw)
	}
}

// billed_total counts Rivian's amount for every counted session that has a
// receipt (even one our estimate could not price); estimated_total is our
// cost over the priced sessions without one; spend.total is their sum.
func TestBilledTotalsSplitTheMonth(t *testing.T) {
	month, _ := ParsePeriod("2026-10", chicago)
	matched := billed(at(priced(charge("m", "2026-10-09T18:00:00Z", time.Hour, 40, 150), 0.40), awayLat, awayLon), 14.25, "usd", "Electrify America", "EA Reno 12", false)
	unpricedButBilled := billed(at(charge("u", "2026-10-20T18:00:00Z", time.Hour, 30, 150), awayLat, awayLon), 20, "USD", "EVgo", "EVgo 7", false)
	homeBilled := billed(priced(charge("h", "2026-10-02T03:00:00Z", 8*time.Hour, 50, 7.5), 0.11), 5.5, "USD", "Rivian", "Garage", true)
	noReceipt := priced(charge("n", "2026-10-11T03:00:00Z", 8*time.Hour, 20, 7.5), 0.11) // 2.20 estimated
	otherCur := billed(priced(charge("c", "2026-10-12T03:00:00Z", 8*time.Hour, 10, 7.5), 0.11), 9, "EUR", "X", "Y", false)
	// Billed in a currency other than the statement's: not billed, counted as estimated.
	m := Build(Input{Charges: []Charge{matched, unpricedButBilled, homeBilled, noReceipt, otherCur}, Config: billedCfg(), Now: ts("2026-10-13T23:00:00Z")}, month, true)

	if m.Spend.BilledTotal == nil || m.Spend.EstimatedTotal == nil || m.BilledSessions == nil || m.EstimatedSessions == nil {
		t.Fatalf("billed fields missing: %+v", m.Spend)
	}
	if got, want := *m.Spend.BilledTotal, round4(14.25+20+5.5); got != want {
		t.Errorf("billed_total = %v, want %v", got, want)
	}
	if got, want := *m.Spend.EstimatedTotal, round4(20*0.11+10*0.11); got != want {
		t.Errorf("estimated_total = %v, want %v", got, want)
	}
	if *m.BilledSessions != 3 || *m.EstimatedSessions != 2 {
		t.Errorf("sessions billed/estimated = %d/%d, want 3/2", *m.BilledSessions, *m.EstimatedSessions)
	}
	// Each session counts once, as its receipt when it has one: the headline is
	// the sum of the two lines, exactly.
	if want := round4(*m.Spend.BilledTotal + *m.Spend.EstimatedTotal); m.Spend.Total != want {
		t.Errorf("spend.total = %v, want billed_total + estimated_total = %v", m.Spend.Total, want)
	}
	if want := round4(14.25 + 20 + 5.5 + 2.2 + 1.1); m.Spend.Total != want {
		t.Errorf("spend.total = %v, want %v", m.Spend.Total, want)
	}
	if got := m.Spend.BySource[SourceRivian]; got != round4(14.25+20+5.5) {
		t.Errorf("by_source.rivian = %v, want the billed amounts", got)
	}
	var kindCost float64
	for _, k := range m.Energy.ByKind {
		kindCost += k.Cost
	}
	if round4(kindCost) != m.Spend.Total {
		t.Errorf("by_kind costs %v do not add up to spend.total %v", kindCost, m.Spend.Total)
	}
	// The unpriced-but-billed session is no longer unpriced.
	if m.Spend.UnpricedSessions != 0 || m.Spend.PricedSessions != 5 {
		t.Errorf("priced/unpriced = %d/%d, want 5/0", m.Spend.PricedSessions, m.Spend.UnpricedSessions)
	}
	// With the flag off the same rows give today's statement.
	off := Build(Input{Charges: []Charge{matched, unpricedButBilled, homeBilled, noReceipt, otherCur}, Config: cfg(), Now: ts("2026-10-13T23:00:00Z")}, month, true)
	if want := round4(40*0.40 + 50*0.11 + 20*0.11 + 10*0.11); off.Spend.Total != want || off.Spend.UnpricedSessions != 1 {
		t.Errorf("flag off: spend.total = %v (want %v), unpriced %d (want 1)", off.Spend.Total, want, off.Spend.UnpricedSessions)
	}

	byID := map[string]ReceiptSession{}
	var home, matchedRcpt Receipt
	for _, r := range m.Receipts {
		for _, s := range r.Sessions {
			byID[s.ChargeID] = s
		}
		if r.Sessions[0].ChargeID == "h" {
			home = r
		}
		if r.Sessions[0].ChargeID == "m" {
			matchedRcpt = r
		}
	}
	if s := byID["m"]; s.Cost == nil || *s.Cost != 14.25 || s.CostSource != SourceRivian {
		t.Errorf("a billed session's cost must be the receipt, source rivian: %+v", s)
	}
	if s := byID["m"]; s.BilledCost == nil || *s.BilledCost != 14.25 || s.BilledCurrency == nil || *s.BilledCurrency != "USD" ||
		s.BilledVendor == nil || *s.BilledVendor != "Electrify America" || s.BilledCharger == nil || *s.BilledCharger != "EA Reno 12" {
		t.Errorf("matched session = %+v", s)
	}
	if s := byID["n"]; s.BilledCost != nil || s.BilledVendor != nil {
		t.Errorf("unmatched session has billed fields: %+v", s)
	}
	if s := byID["c"]; s.BilledCost != nil {
		t.Errorf("a receipt in another currency was treated as billed: %+v", s)
	}
	// A home charger: the amount stays, vendor and charger do not.
	if s := byID["h"]; s.BilledCost == nil || *s.BilledCost != 5.5 || s.BilledVendor != nil || s.BilledCharger != nil {
		t.Errorf("home session = %+v", s)
	}
	if home.BilledVendor != nil || home.BilledCharger != nil || home.BilledCost == nil || *home.BilledCost != 5.5 {
		t.Errorf("home receipt = cost %v vendor %v charger %v", home.BilledCost, home.BilledVendor, home.BilledCharger)
	}
	if matchedRcpt.BilledCost == nil || *matchedRcpt.BilledCost != 14.25 || matchedRcpt.BilledSessions == nil || *matchedRcpt.BilledSessions != 1 ||
		matchedRcpt.EstimatedSessions == nil || *matchedRcpt.EstimatedSessions != 0 || matchedRcpt.BilledVendor == nil || *matchedRcpt.BilledVendor != "Electrify America" {
		t.Errorf("matched receipt = %+v", matchedRcpt)
	}
}

// A plug-in made of fragments: receipt.billed_cost is the sum only when
// every session has a receipt; the counts say how many do.
func TestBilledReceiptSumsOnlyWhenEverySessionMatched(t *testing.T) {
	month, _ := ParsePeriod("2026-10", chicago)
	a := billed(at(priced(charge("a", "2026-10-09T18:00:00Z", 20*time.Minute, 20, 150), 0.40), awayLat, awayLon), 7, "USD", "EVgo", "EVgo 1", false)
	b := billed(at(priced(charge("b", "2026-10-09T18:25:00Z", 20*time.Minute, 20, 150), 0.40), awayLat, awayLon), 6.5, "USD", "EVgo", "EVgo 1", false)
	all := Build(Input{Charges: []Charge{a, b}, Config: billedCfg(), Now: ts("2026-10-13T23:00:00Z")}, month, true)
	if len(all.Receipts) != 1 || all.Receipts[0].SessionCount != 2 {
		t.Fatalf("expected one two-session receipt: %+v", all.Receipts)
	}
	r := all.Receipts[0]
	if r.BilledCost == nil || *r.BilledCost != 13.5 || *r.BilledSessions != 2 || *r.EstimatedSessions != 0 {
		t.Errorf("fully matched receipt = cost %v billed %v estimated %v", r.BilledCost, r.BilledSessions, r.EstimatedSessions)
	}

	b.BilledCost = nil // second fragment has no receipt
	part := Build(Input{Charges: []Charge{a, b}, Config: billedCfg(), Now: ts("2026-10-13T23:00:00Z")}, month, true)
	r = part.Receipts[0]
	if r.BilledCost != nil || *r.BilledSessions != 1 || *r.EstimatedSessions != 1 {
		t.Errorf("partly matched receipt = cost %v billed %v estimated %v; the sum needs every session", r.BilledCost, *r.BilledSessions, *r.EstimatedSessions)
	}
	if r.BilledVendor == nil || *r.BilledVendor != "EVgo" {
		t.Errorf("vendor from the matched session was lost: %v", r.BilledVendor)
	}
}
