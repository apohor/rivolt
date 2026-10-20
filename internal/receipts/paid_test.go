package receipts

import (
	"math"
	"testing"
	"time"
)

func rc(vendor string, daysAgo int, cost, kwh float64, cur string, home *bool) Receipt {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	end := now.AddDate(0, 0, -daysAgo)
	return Receipt{Vendor: vendor, EndedAt: end, StartedAt: end.Add(-time.Hour), TotalCost: &cost, EnergyKWh: &kwh, Currency: cur, IsHome: home}
}

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestPaidRatesNeedThreeSessionsAndAreEnergyWeighted(t *testing.T) {
	rs := []Receipt{
		rc("Electrify America", 5, 30, 50, "USD", nil),  // 0.60
		rc("Electrify America", 20, 10, 20, "USD", nil), // 0.50
		rc("Electrify America", 40, 20, 30, "USD", nil), // 0.667
		rc("EVgo", 3, 20, 40, "USD", nil),               // only two EVgo sessions: no figure
		rc("EVgo", 9, 22, 40, "USD", nil),
	}
	got := PaidRates(rs, now, "USD")
	ea, ok := got["ea"]
	if !ok || ea.Sessions != 3 {
		t.Fatalf("ea = %+v, want 3 sessions", ea)
	}
	if want := 60.0 / 100.0; math.Abs(ea.PerKWh-want) > 1e-9 { // total paid over total kWh, not the mean of 0.60, 0.50, 0.667
		t.Errorf("ea per kWh = %v, want %v (energy-weighted)", ea.PerKWh, want)
	}
	if _, ok := got["evgo"]; ok {
		t.Error("evgo has two receipts and must have no figure")
	}
}

func TestPaidRatesLeaveOutWhatIsNotAPublicPrice(t *testing.T) {
	yes, no := true, false
	var rs []Receipt
	for i := 0; i < 3; i++ {
		rs = append(rs,
			rc("Electrify America", 5+i, 30, 50, "USD", &yes),  // home charger: not a network price
			rc("Electrify America", 5+i, 0, 50, "USD", &no),    // free or unpriced
			rc("Electrify America", 5+i, 30, 0, "USD", &no),    // no energy
			rc("Electrify America", 5+i, 30, 50, "CAD", &no),   // another currency
			rc("Electrify America", 400+i, 30, 50, "USD", &no), // over 12 months old
			rc("Some Local Operator", 5+i, 30, 50, "USD", &no), // matches no network
		)
	}
	if got := PaidRates(rs, now, "USD"); len(got) != 0 {
		t.Errorf("figures from receipts that are not a usable network price: %+v", got)
	}
}

func TestPaidRatesUseTheMostRecentTen(t *testing.T) {
	var rs []Receipt
	for i := 0; i < 10; i++ { // recent: 0.50
		rs = append(rs, rc("EVgo", 1+i, 25, 50, "USD", nil))
	}
	for i := 0; i < 5; i++ { // older and dearer: ignored
		rs = append(rs, rc("EVgo", 100+i, 50, 50, "USD", nil))
	}
	got := PaidRates(rs, now, "USD")["evgo"]
	if got.Sessions != PaidMaxSessions || math.Abs(got.PerKWh-0.5) > 1e-9 {
		t.Errorf("evgo = %+v, want the 10 recent sessions at 0.50", got)
	}
}
