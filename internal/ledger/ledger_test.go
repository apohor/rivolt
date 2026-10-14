package ledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

var chicago = mustLoc("America/Chicago")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// home is the saved home location used across tests; homeLat/Lon put a
// session at it, awayLat/Lon ~645 km away.
var home = &LatLon{Lat: 30.27, Lon: -97.74}

const (
	homeLat, homeLon = 30.2701, -97.7401
	awayLat, awayLon = 35.47, -97.52
)

func charge(id string, start string, dur time.Duration, kwh, peak float64) Charge {
	st := ts(start)
	return Charge{
		ID: id, VehicleID: "veh-1",
		StartedAt: st, EndedAt: st.Add(dur),
		StartSoC: f(40), EndSoC: f(70),
		EnergyKWh: f(kwh), PeakKW: f(peak),
		FinalState: "charging_complete", Source: "live",
		Lat: f(homeLat), Lon: f(homeLon),
		CreatedAt: st,
	}
}

func priced(c Charge, rate float64) Charge {
	c.PricePerKWh = f(rate)
	c.Cost = f(rate * *c.EnergyKWh)
	c.Currency = "USD"
	return c
}

func at(c Charge, lat, lon float64) Charge {
	c.Lat, c.Lon = f(lat), f(lon)
	return c
}

func cfg() Config { return Config{Currency: "USD", HomeRate: 0.11, Home: home} }

func TestParsePeriod_UsesTimeZone(t *testing.T) {
	p, err := ParsePeriod("2026-10", chicago)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Start, ts("2026-10-01T05:00:00Z"); !got.Equal(want) {
		t.Errorf("start = %s, want %s", got, want)
	}
	// Nov 1 00:00 local is still CDT (DST ends at 02:00 that day).
	if got, want := p.End, ts("2026-11-01T05:00:00Z"); !got.Equal(want) {
		t.Errorf("end = %s, want %s", got, want)
	}
	nov, _ := ParsePeriod("2026-11", chicago)
	if got, want := nov.End, ts("2026-12-01T06:00:00Z"); !got.Equal(want) {
		t.Errorf("nov end = %s, want %s (CST)", got, want)
	}
	for _, bad := range []string{"", "2026-13", "2026/10", "Oct 2026", "2026-1"} {
		if _, err := ParsePeriod(bad, time.UTC); err == nil {
			t.Errorf("ParsePeriod(%q) accepted", bad)
		}
	}
}

func TestLastMonths(t *testing.T) {
	ps := LastMonths(ts("2026-01-15T12:00:00Z"), 3, time.UTC)
	var got []string
	for _, p := range ps {
		got = append(got, p.Month)
	}
	if want := []string{"2025-11", "2025-12", "2026-01"}; !reflect.DeepEqual(got, want) {
		t.Errorf("months = %v, want %v", got, want)
	}
}

func TestCostSource_LegacyDerivation(t *testing.T) {
	ac := charge("ac", "2026-10-02T00:00:00Z", time.Hour, 10, 7)
	dc := charge("dc", "2026-10-02T00:00:00Z", 30*time.Minute, 50, 150)
	cases := []struct {
		name string
		c    Charge
		home float64
		want string
	}{
		{"unpriced", ac, 0.11, SourceUnknown},
		{"home rate on AC", priced(ac, 0.11), 0.11, SourceHomeRate},
		{"within 0.0005", priced(ac, 0.1104), 0.11, SourceHomeRate},
		{"outside 0.0005", priced(ac, 0.1106), 0.11, SourceRivian},
		{"home rate on DCFC", priced(dc, 0.11), 0.11, SourceHomeRateMisapplied},
		{"billed DCFC", priced(dc, 0.4837), 0.11, SourceRivian},
		{"no home rate set", priced(ac, 0.11), 0, SourceRivian},
	}
	for _, tc := range cases {
		got, derived := CostSource(tc.c, tc.home)
		if got != tc.want || !derived {
			t.Errorf("%s: CostSource = %s (derived %v), want %s (derived)", tc.name, got, derived, tc.want)
		}
	}
}

func TestCostSource_PersistedWins(t *testing.T) {
	dc := priced(charge("dc", "2026-10-02T00:00:00Z", 30*time.Minute, 50, 150), 0.11)
	for _, src := range []string{SourceRivian, SourceHomeRate, SourceNetworkEstimate, SourceUser} {
		c := dc
		c.CostSource = src
		if got, derived := CostSource(c, 0.11); got != src || derived {
			t.Errorf("persisted %s: got %s derived %v", src, got, derived)
		}
	}
	free := dc
	free.CostSource, free.Cost = SourceUser, f(0)
	if got, _ := CostSource(free, 0.11); got != SourceUser {
		t.Errorf("user $0 (free) = %s, want user", got)
	}
	noCost := dc
	noCost.CostSource, noCost.Cost = SourceNetworkEstimate, nil
	if got, derived := CostSource(noCost, 0.11); got != SourceUnknown || derived {
		t.Errorf("persisted source without cost = %s derived %v, want unknown", got, derived)
	}
	odd := dc
	odd.CostSource = "something_new"
	if got, derived := CostSource(odd, 0.11); got != SourceHomeRateMisapplied || !derived {
		t.Errorf("unrecognised persisted source must fall back to derivation, got %s %v", got, derived)
	}
}

// The prod May 8 duplicate: two live recorders opened the same session
// a second apart. Exactly one survives, every total counts it once, and
// the loser is reported with its winner.
func TestEffectiveCharges_SupersedesOverlappingDuplicate(t *testing.T) {
	a := priced(charge("live_v_c_1778280860", "2026-05-08T22:54:20Z", 156*time.Minute, 18.1, 7.5), 0.11)
	b := priced(charge("live_v_c_1778280861", "2026-05-08T22:54:21Z", 156*time.Minute, 18.5, 7.5), 0.11)
	b.CreatedAt = a.CreatedAt.Add(time.Second)
	kept, lost := EffectiveCharges([]Charge{b, a})
	if len(kept) != 1 || kept[0].ID != a.ID {
		t.Fatalf("kept = %+v, want only %s (created first)", ids(kept), a.ID)
	}
	if want := []Superseded{{ChargeID: b.ID, SupersededBy: a.ID}}; !reflect.DeepEqual(lost, want) {
		t.Errorf("superseded = %+v, want %+v", lost, want)
	}
}

func TestEffectiveCharges_Priority(t *testing.T) {
	live := charge("live", "2026-10-02T00:00:00Z", time.Hour, 10, 7)
	imp := charge("imp", "2026-10-01T23:30:00Z", 3*time.Hour, 30, 7)
	imp.Source = "electrafi_import"
	kept, _ := EffectiveCharges([]Charge{imp, live})
	if len(kept) != 1 || kept[0].ID != "live" {
		t.Errorf("live must beat a longer import: kept %v", ids(kept))
	}

	noEnergy := charge("a", "2026-10-02T00:00:00Z", 2*time.Hour, 0, 7)
	noEnergy.EnergyKWh = nil
	withEnergy := charge("b", "2026-10-02T00:10:00Z", time.Hour, 9, 7)
	kept, _ = EffectiveCharges([]Charge{noEnergy, withEnergy})
	if len(kept) != 1 || kept[0].ID != "b" {
		t.Errorf("energy must beat duration: kept %v", ids(kept))
	}
}

func TestEffectiveCharges_KeepsAdjacentAndOtherVehicles(t *testing.T) {
	a := charge("a", "2026-10-02T00:00:00Z", time.Hour, 10, 7)
	b := charge("b", "2026-10-02T01:00:00Z", time.Hour, 10, 7) // starts as a ends
	c := charge("c", "2026-10-02T00:30:00Z", time.Hour, 10, 7)
	c.VehicleID = "veh-2" // overlaps a in time, different car
	kept, lost := EffectiveCharges([]Charge{a, b, c})
	if len(kept) != 3 || len(lost) != 0 {
		t.Errorf("kept %v lost %v; want all 3 kept", ids(kept), lost)
	}
}

func TestBuild_SpendBySourceAndQuality(t *testing.T) {
	month, _ := ParsePeriod("2026-10", chicago)
	homeL2 := priced(charge("home", "2026-10-02T03:00:00Z", 8*time.Hour, 60, 7.5), 0.11)
	dcfcStamped := at(priced(charge("dcfc", "2026-10-09T18:00:00Z", 30*time.Minute, 70, 180), 0.11), awayLat, awayLon)
	ran := at(priced(charge("ran", "2026-10-10T18:00:00Z", 30*time.Minute, 50, 150), 0.48), awayLat, awayLon)
	awayL1 := at(charge("l1", "2026-10-11T02:00:00Z", 10*time.Hour, 20, 1.4), awayLat, awayLon)
	euro := priced(charge("eur", "2026-10-12T18:00:00Z", time.Hour, 10, 11), 0.30)
	euro.Currency = "EUR"
	dup := priced(charge("dup", "2026-10-02T03:00:01Z", 8*time.Hour, 61, 7.5), 0.11)
	dup.CreatedAt = homeL2.CreatedAt.Add(time.Second)
	outside := priced(charge("sep", "2026-09-20T18:00:00Z", time.Hour, 40, 7), 0.11)

	in := Input{
		Charges: []Charge{homeL2, dcfcStamped, ran, awayL1, euro, dup, outside},
		Config:  cfg(),
		Now:     ts("2026-10-13T23:00:00Z"),
	}
	m := Build(in, month, true)

	// The DCFC stamped with the home rate is a legacy misapplication:
	// estimate pending, out of spend.
	wantTotal := round4(60*0.11 + 50*0.48)
	if m.Spend.Total != wantTotal {
		t.Errorf("spend.total = %v, want %v", m.Spend.Total, wantTotal)
	}
	if got := m.Spend.BySource[SourceRivian]; got != round4(50*0.48) {
		t.Errorf("by_source.rivian = %v", got)
	}
	if got := m.Spend.BySource[SourceHomeRate]; got != round4(60*0.11) {
		t.Errorf("by_source.home_rate = %v", got)
	}
	if m.Spend.EstimatePendingSessions != 1 || m.Spend.EstimatePendingKWh != 70 {
		t.Errorf("estimate pending = %d / %v kWh, want 1 / 70", m.Spend.EstimatePendingSessions, m.Spend.EstimatePendingKWh)
	}
	if m.Spend.EstimatedShare == nil || *m.Spend.EstimatedShare != 0 {
		t.Errorf("estimated_share = %v, want 0 (no network estimates)", m.Spend.EstimatedShare)
	}
	if m.Spend.PricedSessions != 2 || m.Spend.UnpricedSessions != 1 || m.Spend.UnpricedKWh != 20 {
		t.Errorf("priced/unpriced = %d/%d kwh %v", m.Spend.PricedSessions, m.Spend.UnpricedSessions, m.Spend.UnpricedKWh)
	}
	if m.Spend.UnpricedHomeRateEstimate == nil || *m.Spend.UnpricedHomeRateEstimate != round4(20*0.11) {
		t.Errorf("unpriced estimate = %v", m.Spend.UnpricedHomeRateEstimate)
	}
	if m.Energy.Sessions != 5 || m.Energy.KWhAdded != 210 {
		t.Errorf("energy = %d sessions %v kWh, want 5 / 210", m.Energy.Sessions, m.Energy.KWhAdded)
	}
	if dc := m.Energy.ByKind["dcfc"]; dc.Sessions != 2 || dc.KWh != 120 || dc.PeakKW == nil || *dc.PeakKW != 180 ||
		dc.Cost != round4(50*0.48) {
		t.Errorf("by_kind.dcfc = %+v", dc)
	}
	// by_place classifies per session and reconciles with the totals.
	hp, ap := m.Energy.ByPlace["home"], m.Energy.ByPlace["away"]
	if hp.Sessions != 2 || hp.KWh != 70 || ap.Sessions != 3 || ap.KWh != 140 || m.Energy.ByPlace["unknown"].Sessions != 0 {
		t.Errorf("by_place home %+v away %+v", hp, ap)
	}
	if hp.Cost+ap.Cost != m.Spend.Total {
		t.Errorf("by_place cost %v + %v != spend %v", hp.Cost, ap.Cost, m.Spend.Total)
	}
	assertPlaceKindReconciles(t, m)
	if c := m.Energy.ByPlaceKind["away"]["dcfc"]; c.Sessions != 2 || c.KWh != 120 || c.Cost != round4(50*0.48) {
		t.Errorf("by_place_kind.away.dcfc = %+v", c)
	}
	if c := m.Energy.ByPlaceKind["away"]["ac"]; c.Sessions != 1 || c.KWh != 20 || c.Cost != 0 {
		t.Errorf("by_place_kind.away.ac = %+v", c)
	}
	dq := m.DataQuality
	if !dq.CostSourceInferred || dq.CostSourceDerivedSessions != 5 || dq.SupersededExcluded != 1 || dq.HomeRateOnDCFC != 1 ||
		dq.HomeRateAway != 1 || dq.CurrencyMismatchExcluded != 1 {
		t.Errorf("data_quality = %+v", dq)
	}
	if len(dq.Superseded) != 1 || dq.Superseded[0] != (Superseded{ChargeID: "dup", SupersededBy: "home"}) {
		t.Errorf("superseded = %+v", dq.Superseded)
	}
	if !m.IsCurrentMonth {
		t.Error("is_current_month = false")
	}
	// UX relies on receipts reconciling with spend: receipt cost sums only
	// sessions that count in spend.total; a misapplied session keeps its
	// stamped cost on the session row but stays out of the receipt cost.
	var receiptSum float64
	for _, r := range m.Receipts {
		if r.Cost != nil {
			receiptSum += *r.Cost
		}
		for _, sess := range r.Sessions {
			if sess.CostSource == SourceHomeRateMisapplied && (sess.Cost == nil || *sess.Cost != round4(70*0.11)) {
				t.Errorf("misapplied session must keep its stamped cost: %+v", sess)
			}
		}
		// "dcfc" (misapplied), "ran" (billed) and "l1" (unpriced) are one plug-in:
		// same place, no drive between. Only the billed session counts.
		if r.ReceiptID == "rcpt_dcfc" && (r.Cost == nil || *r.Cost != round4(50*0.48) ||
			r.SessionCount != 3 || r.CostSource != SourceMixed) {
			t.Errorf("mixed receipt = cost %v sessions %d source %s", r.Cost, r.SessionCount, r.CostSource)
		}
	}
	if round4(receiptSum) != m.Spend.Total {
		t.Errorf("Σ receipt cost %v != spend.total %v", round4(receiptSum), m.Spend.Total)
	}
	if m.FirstActivityAt == nil || !m.FirstActivityAt.Equal(homeL2.StartedAt) {
		t.Errorf("first_activity_at = %v", m.FirstActivityAt)
	}
}

func TestBuild_ReceiptsGroupByPlugIn(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	// One plug-in at home: a session, then two top-ups, no drive between.
	s1 := priced(charge("s1", "2026-10-12T23:00:00Z", 6*time.Hour, 40, 7.6), 0.11)
	s2 := priced(charge("s2", "2026-10-13T06:00:00Z", 10*time.Minute, 0.8, 7.6), 0.11)
	s3 := charge("s3", "2026-10-13T09:00:00Z", 5*time.Minute, 0, 7.6)
	s3.EnergyKWh = nil
	// A drive, then a DCFC stop away from home.
	drive := Drive{ID: "d1", VehicleID: "veh-1", StartedAt: ts("2026-10-13T12:00:00Z"),
		EndedAt: ts("2026-10-13T14:00:00Z"), DistanceMi: f(120), EnergyKWh: f(45), Source: "live"}
	fast := at(priced(charge("f1", "2026-10-13T14:10:00Z", 25*time.Minute, 60, 190), 0.11), awayLat, awayLon)

	m := Build(Input{Charges: []Charge{s1, s2, s3, fast}, Drives: []Drive{drive}, Config: cfg(),
		Now: ts("2026-10-14T00:00:00Z")}, month, true)
	if len(m.Receipts) != 2 {
		t.Fatalf("receipts = %d, want 2: %+v", len(m.Receipts), m.Receipts)
	}
	// Newest first.
	dc, plug := m.Receipts[0], m.Receipts[1]
	if dc.ReceiptID != "rcpt_f1" || dc.Kind != "dcfc" || dc.Place != "away" || dc.CostSource != SourceHomeRateMisapplied ||
		!hasFlag(dc.Flags, FlagHomeRateOnDCFC) || !hasFlag(dc.Flags, FlagHomeRateAway) || !hasFlag(dc.Flags, FlagEstimatePending) {
		t.Errorf("dcfc receipt = %+v", dc)
	}
	// UX counts under-priced sessions from session-level flags.
	if sf := dc.Sessions[0].Flags; !hasFlag(sf, FlagHomeRateOnDCFC) || !hasFlag(sf, FlagHomeRateAway) {
		t.Errorf("session flags = %v, want home_rate_on_dcfc + home_rate_away", sf)
	}
	if s0 := dc.Sessions[0]; s0.Kind != "dcfc" || s0.Place != "away" || !s0.CostSourceDerived || s0.Network != nil {
		t.Errorf("session = %+v", s0)
	}
	if plug.ReceiptID != "rcpt_s1" || plug.SessionCount != 3 || plug.Kind != "ac" || plug.Place != "home" {
		t.Errorf("plug-in receipt = %+v", plug)
	}
	if plug.KWh != 40.8 || plug.UnpricedSessions != 1 || plug.CostSource != SourceMixed {
		t.Errorf("plug-in totals: kwh %v unpriced %d source %s", plug.KWh, plug.UnpricedSessions, plug.CostSource)
	}
	if plug.Cost == nil || *plug.Cost != round4(40.8*0.11) || plug.Rate == nil || *plug.Rate != 0.11 {
		t.Errorf("plug-in cost %v rate %v", plug.Cost, plug.Rate)
	}
	for _, fl := range []string{FlagFragmentsFolded, FlagUnpriced, FlagNullEnergy} {
		if !hasFlag(plug.Flags, fl) {
			t.Errorf("plug-in flags %v missing %s", plug.Flags, fl)
		}
	}
	if plug.SpansDays != 2 || plug.LocalDate != "2026-10-12" {
		t.Errorf("spans_days %d local_date %s", plug.SpansDays, plug.LocalDate)
	}
	if *plug.SoCStart != 40 || *plug.SoCEnd != 70 {
		t.Errorf("soc %v→%v", *plug.SoCStart, *plug.SoCEnd)
	}
	if m.Driving.Miles != 120 || m.Driving.Coverage == nil || *m.Driving.Coverage != 1 {
		t.Errorf("driving = %+v", m.Driving)
	}
}

// Month assignment follows the user's zone, and a plug-in that crosses
// the boundary is split with continuity flags on both sides.
func TestBuild_MonthBoundaryInTimeZone(t *testing.T) {
	// 2026-10-01T03:00Z is Sep 30 22:00 in Chicago.
	late := priced(charge("late", "2026-10-01T03:00:00Z", 2*time.Hour, 15, 7.5), 0.11)
	// Continues after local midnight: Oct 1 01:30 CDT.
	next := priced(charge("next", "2026-10-01T06:30:00Z", 3*time.Hour, 20, 7.5), 0.11)
	in := Input{Charges: []Charge{late, next}, Config: cfg(), Now: ts("2026-10-13T00:00:00Z")}

	sep, _ := ParsePeriod("2026-09", chicago)
	oct, _ := ParsePeriod("2026-10", chicago)
	ms, mo := Build(in, sep, true), Build(in, oct, true)
	if ms.Energy.KWhAdded != 15 || mo.Energy.KWhAdded != 20 {
		t.Errorf("Chicago split: sep %v oct %v, want 15/20", ms.Energy.KWhAdded, mo.Energy.KWhAdded)
	}
	if len(ms.Receipts) != 1 || !hasFlag(ms.Receipts[0].Flags, FlagContinuesNextMonth) {
		t.Errorf("sep receipt flags = %+v", ms.Receipts)
	}
	if len(mo.Receipts) != 1 || !hasFlag(mo.Receipts[0].Flags, FlagContinuedFromPrev) {
		t.Errorf("oct receipt flags = %+v", mo.Receipts)
	}

	octUTC, _ := ParsePeriod("2026-10", time.UTC)
	if got := Build(in, octUTC, false).Energy.KWhAdded; got != 35 {
		t.Errorf("UTC October kWh = %v, want 35 (both sessions)", got)
	}
}

func TestBuild_DrivingCoverageAndDedupe(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	ds := []Drive{
		{ID: "a", VehicleID: "veh-1", StartedAt: ts("2026-10-02T10:00:00Z"), EndedAt: ts("2026-10-02T11:00:00Z"),
			DistanceMi: f(30), EnergyKWh: f(12), Source: "live"},
		{ID: "b", VehicleID: "veh-1", StartedAt: ts("2026-10-03T10:00:00Z"), EndedAt: ts("2026-10-03T12:00:00Z"),
			DistanceMi: f(70), Source: "electrafi_import"},
		// Fragment overlapping "a" from a second recorder.
		{ID: "a2", VehicleID: "veh-1", StartedAt: ts("2026-10-02T10:20:00Z"), EndedAt: ts("2026-10-02T10:40:00Z"),
			DistanceMi: f(9), EnergyKWh: f(4), Source: "live", CreatedAt: ts("2026-10-02T10:20:00Z")},
	}
	cs := []Charge{priced(charge("c", "2026-10-04T00:00:00Z", time.Hour, 50, 7), 0.11)}
	m := Build(Input{Charges: cs, Drives: ds, Config: cfg(), Now: ts("2026-10-20T00:00:00Z")}, month, false)
	if m.Driving.Drives != 2 || m.Driving.Miles != 100 || m.Driving.MilesWithEnergy != 30 {
		t.Errorf("driving = %+v", m.Driving)
	}
	if m.Driving.Coverage == nil || *m.Driving.Coverage != 0.3 {
		t.Errorf("coverage = %v, want 0.3", m.Driving.Coverage)
	}
	if m.Driving.CostPerMile == nil || *m.Driving.CostPerMile != round4(50*0.11/100) {
		t.Errorf("cost_per_mile = %v", m.Driving.CostPerMile)
	}
	if m.DataQuality.DrivesSupersededExcluded != 1 {
		t.Errorf("drives superseded = %d, want 1", m.DataQuality.DrivesSupersededExcluded)
	}
	if m.Receipts != nil {
		t.Errorf("summary build must not carry receipts")
	}
}

func TestBuild_EmptyMonthShape(t *testing.T) {
	month, _ := ParsePeriod("2026-08", time.UTC)
	m := Build(Input{Config: Config{Currency: "USD"}, Now: ts("2026-10-01T00:00:00Z")}, month, true)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"version":1`, `"receipts":[]`,
		`"by_source":{"home_rate":0,"network_estimate":0,"rivian":0,"user":0}`,
		`"by_place":{"away":`, `"by_place_kind":{"away":{"ac":`, `"estimated_share":null`, `"estimate_pending_sessions":0`,
		`"coverage":null`, `"cost_per_mile":null`, `"unpriced_home_rate_estimate":null`,
		`"first_activity_at":null`, `"is_current_month":false`, `"cost_source_inferred":false`,
		`"cost_per_mile_basis":"spend_over_miles"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s:\n%s", want, s)
		}
	}
	sum, _ := json.Marshal(MonthSummary{Monthly: m})
	if strings.Contains(string(sum), `"receipts"`) {
		t.Errorf("summary JSON must omit receipts: %s", sum)
	}
}

// Rows priced by the pricing-aware recorder carry cost_source and
// network; they count in spend as-is and surface the network.
func TestBuild_PersistedNetworkEstimate(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	ea := at(priced(charge("ea", "2026-10-09T18:00:00Z", 30*time.Minute, 70, 180), 0.36), awayLat, awayLon)
	ea.CostSource, ea.Network = SourceNetworkEstimate, "Electrify America"
	topUp := at(priced(charge("ea2", "2026-10-09T18:40:00Z", 10*time.Minute, 5, 40), 0.36), awayLat, awayLon)
	topUp.CostSource, topUp.Network = SourceNetworkEstimate, "Electrify America"
	home := priced(charge("home", "2026-10-10T03:00:00Z", 8*time.Hour, 60, 7.5), 0.11)
	home.CostSource = SourceHomeRate
	user := at(charge("l1", "2026-10-11T02:00:00Z", 10*time.Hour, 20, 1.4), awayLat, awayLon)
	user.CostSource, user.Cost = SourceUser, f(0) // marked free

	m := Build(Input{Charges: []Charge{ea, topUp, home, user}, Config: cfg(), Now: ts("2026-10-20T00:00:00Z")}, month, true)
	for _, r := range m.Receipts {
		if r.ReceiptID == "rcpt_l1" && (!hasFlag(r.Flags, FlagFree) || !hasFlag(r.Sessions[0].Flags, FlagFree) ||
			hasFlag(r.Flags, FlagUnpriced) || r.Cost == nil || *r.Cost != 0 || r.CostSource != SourceUser) {
			t.Errorf("free receipt = %+v", r)
		}
		if r.ReceiptID != "rcpt_l1" && hasFlag(r.Flags, FlagFree) {
			t.Errorf("only the $0 user session is free, got %s %v", r.ReceiptID, r.Flags)
		}
	}
	if m.Spend.BySource[SourceNetworkEstimate] != round4(75*0.36) || m.Spend.BySource[SourceUser] != 0 ||
		m.Spend.PricedSessions != 4 || m.Spend.UnpricedSessions != 0 {
		t.Errorf("spend = %+v", m.Spend)
	}
	wantShare := round4(75 * 0.36 / (75*0.36 + 60*0.11))
	if m.Spend.EstimatedShare == nil || *m.Spend.EstimatedShare != wantShare {
		t.Errorf("estimated_share = %v, want %v", m.Spend.EstimatedShare, wantShare)
	}
	if m.DataQuality.CostSourceInferred || m.DataQuality.CostSourceDerivedSessions != 0 || m.DataQuality.HomeRateOnDCFC != 0 {
		t.Errorf("persisted rows must not count as inferred: %+v", m.DataQuality)
	}
	var trip *Receipt
	for i := range m.Receipts {
		if m.Receipts[i].ReceiptID == "rcpt_ea" {
			trip = &m.Receipts[i]
		}
	}
	if trip == nil || trip.Network == nil || *trip.Network != "Electrify America" || trip.SessionCount != 2 ||
		trip.CostSource != SourceNetworkEstimate {
		t.Fatalf("network receipt = %+v", trip)
	}
	// A DCFC receipt can fold an AC-peak session; kinds stay per session.
	if trip.Kind != "dcfc" || trip.Sessions[1].Kind != "ac" {
		t.Errorf("receipt kind %s, second session kind %s", trip.Kind, trip.Sessions[1].Kind)
	}
	if ac := m.Energy.ByKind["ac"]; ac.Sessions != 3 {
		t.Errorf("by_kind.ac sessions = %d, want 3 (per-session classification)", ac.Sessions)
	}
	assertPlaceKindReconciles(t, m)
}

// assertPlaceKindReconciles checks the by_place_kind invariants from
// contract §11: rows sum to by_kind, columns to by_place, and cell costs
// to the cost already attributed by_place.
func assertPlaceKindReconciles(t *testing.T, m Monthly) {
	t.Helper()
	places, kinds := []string{"home", "away", "unknown"}, []string{"ac", "dcfc"}
	for _, k := range kinds {
		var sum KindLine
		for _, p := range places {
			c, ok := m.Energy.ByPlaceKind[p][k]
			if !ok {
				t.Fatalf("by_place_kind missing %s.%s", p, k)
			}
			sum.Sessions += c.Sessions
			sum.KWh += c.KWh
			sum.Cost += c.Cost
		}
		if want := m.Energy.ByKind[k]; sum.Sessions != want.Sessions || round4(sum.KWh) != want.KWh || round4(sum.Cost) != want.Cost {
			t.Errorf("Σ places of %s = %+v, by_kind = %+v", k, sum, want)
		}
	}
	for _, p := range places {
		var sum KindLine
		for _, k := range kinds {
			c := m.Energy.ByPlaceKind[p][k]
			sum.Sessions += c.Sessions
			sum.KWh += c.KWh
			sum.Cost += c.Cost
		}
		if want := m.Energy.ByPlace[p]; sum.Sessions != want.Sessions || round4(sum.KWh) != want.KWh || round4(sum.Cost) != want.Cost {
			t.Errorf("Σ kinds of %s = %+v, by_place = %+v", p, sum, want)
		}
	}
}

func ids(cs []Charge) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}
