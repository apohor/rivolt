package ledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
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
		got, derived := CostSource(tc.c, Config{HomeRate: tc.home, Home: home})
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
		if got, derived := CostSource(c, Config{HomeRate: 0.11, Home: home}); got != src || derived {
			t.Errorf("persisted %s: got %s derived %v", src, got, derived)
		}
	}
	free := dc
	free.CostSource, free.Cost = SourceUser, f(0)
	if got, _ := CostSource(free, Config{HomeRate: 0.11, Home: home}); got != SourceUser {
		t.Errorf("user $0 (free) = %s, want user", got)
	}
	noCost := dc
	noCost.CostSource, noCost.Cost = SourceNetworkEstimate, nil
	if got, derived := CostSource(noCost, Config{HomeRate: 0.11, Home: home}); got != SourceUnknown || derived {
		t.Errorf("persisted source without cost = %s derived %v, want unknown", got, derived)
	}
	odd := dc
	odd.CostSource = "something_new"
	if got, derived := CostSource(odd, Config{HomeRate: 0.11, Home: home}); got != SourceHomeRateMisapplied || !derived {
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
	// The only unpriced session is AC away from home: no "at your home
	// rate" hint for it.
	if m.Spend.UnpricedHomeRateEstimate == nil || *m.Spend.UnpricedHomeRateEstimate != 0 {
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
	// s3 is a zero-energy crumb: it neither makes the receipt "mixed" nor
	// counts as unpriced (D2 F2).
	if plug.KWh != 40.8 || plug.UnpricedSessions != 0 || plug.CostSource != SourceHomeRate {
		t.Errorf("plug-in totals: kwh %v unpriced %d source %s", plug.KWh, plug.UnpricedSessions, plug.CostSource)
	}
	if plug.Cost == nil || *plug.Cost != round4(40.8*0.11) || plug.Rate == nil || *plug.Rate != 0.11 {
		t.Errorf("plug-in cost %v rate %v", plug.Cost, plug.Rate)
	}
	for _, fl := range []string{FlagFragmentsFolded, FlagNullEnergy} {
		if !hasFlag(plug.Flags, fl) {
			t.Errorf("plug-in flags %v missing %s", plug.Flags, fl)
		}
	}
	if hasFlag(plug.Flags, FlagUnpriced) {
		t.Errorf("a zero-energy crumb must not mark a priced receipt unpriced: %v", plug.Flags)
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
		`"by_source":{"home_rate":0,"network_estimate":0,"observed_estimate":0,"rivian":0,"user":0}`,
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

// A5 / D2 F1: the ledger uses the recorder's DCFC rule. A 15-hour home
// L2 session with one glitched 145 kW sample is AC at the home rate, not
// a DC fast charge stuck at "estimate pending".
func TestBuild_GlitchedPeakIsNotDCFC(t *testing.T) {
	month, _ := ParsePeriod("2026-09", time.UTC)
	glitch := priced(charge("glitch", "2026-09-14T02:00:00Z", 15*time.Hour, 66, 145), 0.11)
	m := Build(Input{Charges: []Charge{glitch}, Config: cfg(), Now: ts("2026-10-01T00:00:00Z")}, month, true)
	if m.Energy.ByKind["dcfc"].Sessions != 0 || m.Spend.EstimatePendingSessions != 0 || m.Spend.Total != round4(66*0.11) {
		t.Errorf("glitched home session: by_kind %+v pending %d total %v", m.Energy.ByKind, m.Spend.EstimatePendingSessions, m.Spend.Total)
	}
	if r := m.Receipts[0]; r.Kind != "ac" || r.CostSource != SourceHomeRate || hasFlag(r.Flags, FlagEstimatePending) {
		t.Errorf("receipt = kind %s source %s flags %v", r.Kind, r.CostSource, r.Flags)
	}
}

// D2 F1, the preview case: a 4-second, 0.12 kWh fragment with a 114 kW
// sample inside a home plug-in must not turn the receipt into
// "DCFC at home · estimate pending".
func TestBuild_TinyGlitchedFragmentStaysHome(t *testing.T) {
	month, _ := ParsePeriod("2026-09", time.UTC)
	main := priced(charge("main", "2026-09-29T22:00:00Z", 6*time.Hour, 40, 7.6), 0.11)
	frag := priced(charge("frag", "2026-09-30T04:30:00Z", 4*time.Second, 0.12, 114.5), 0.11)
	m := Build(Input{Charges: []Charge{main, frag}, Config: cfg(), Now: ts("2026-10-01T00:00:00Z")}, month, true)
	if len(m.Receipts) != 1 {
		t.Fatalf("receipts = %d, want 1", len(m.Receipts))
	}
	r := m.Receipts[0]
	if r.Kind != "ac" || r.CostSource != SourceHomeRate || hasFlag(r.Flags, FlagEstimatePending) ||
		m.Spend.EstimatePendingSessions != 0 || m.Energy.ByKind["dcfc"].Sessions != 0 {
		t.Errorf("receipt kind %s source %s flags %v; pending %d", r.Kind, r.CostSource, r.Flags, m.Spend.EstimatePendingSessions)
	}
}

// A5 / D2 F3 (owner decision): AC charging away from home is unknown
// until priced, whether the home rate was stamped by the old recorder
// (legacy) or by #62's (persisted home_rate). It needs a price, stays
// out of spend and out of the "at your home rate" hint.
func TestBuild_AwayACIsUnknown(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	legacy := at(priced(charge("legacy", "2026-10-11T02:00:00Z", 10*time.Hour, 20, 1.4), 0.11), awayLat, awayLon)
	persisted := at(priced(charge("persisted", "2026-10-12T02:00:00Z", 10*time.Hour, 30, 1.4), 0.11), awayLat, awayLon)
	persisted.CostSource = SourceHomeRate
	homeAC := priced(charge("home", "2026-10-13T02:00:00Z", 6*time.Hour, 40, 7.6), 0.11)
	homeUnpriced := charge("home-unpriced", "2026-10-14T02:00:00Z", 2*time.Hour, 10, 7.6)
	dcUnpriced := at(charge("dc-unpriced", "2026-10-15T18:00:00Z", 30*time.Minute, 50, 150), awayLat, awayLon)

	m := Build(Input{Charges: []Charge{legacy, persisted, homeAC, homeUnpriced, dcUnpriced}, Config: cfg(),
		Now: ts("2026-10-20T00:00:00Z")}, month, true)
	if m.Spend.Total != round4(40*0.11) || m.Spend.BySource[SourceHomeRate] != round4(40*0.11) {
		t.Errorf("spend = %+v, want only the home session", m.Spend)
	}
	if m.Spend.UnpricedSessions != 4 || m.Spend.UnpricedKWh != 110 {
		t.Errorf("unpriced = %d / %v kWh, want 4 / 110", m.Spend.UnpricedSessions, m.Spend.UnpricedKWh)
	}
	// Hint covers only AC that could have been at home.
	if est := m.Spend.UnpricedHomeRateEstimate; est == nil || *est != round4(10*0.11) {
		t.Errorf("home-rate hint = %v, want %v (home AC only)", est, round4(10*0.11))
	}
	if m.DataQuality.HomeRateAway != 2 {
		t.Errorf("home_rate_away = %d, want 2", m.DataQuality.HomeRateAway)
	}
	for _, r := range m.Receipts {
		for _, sess := range r.Sessions {
			if sess.ChargeID != "legacy" && sess.ChargeID != "persisted" {
				continue
			}
			if sess.CostSource != SourceUnknown || !sess.CostSourceDerived ||
				!hasFlag(sess.Flags, FlagUnpriced) || !hasFlag(sess.Flags, FlagHomeRateAway) {
				t.Errorf("away AC session %s = source %s derived %v flags %v", sess.ChargeID, sess.CostSource, sess.CostSourceDerived, sess.Flags)
			}
		}
	}
}

// A5 / D2 F2: zero-energy crumbs are null_energy, not "needs a price".
func TestBuild_ZeroEnergyCrumbsAreNotUnpriced(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	crumb := charge("crumb", "2026-10-02T00:00:00Z", 30*time.Second, 0, 7)
	crumb.EnergyKWh = nil
	zero := charge("zero", "2026-10-03T00:00:00Z", time.Minute, 0, 7)
	m := Build(Input{Charges: []Charge{crumb, zero}, Config: cfg(), Now: ts("2026-10-20T00:00:00Z")}, month, true)
	if m.Spend.UnpricedSessions != 0 || m.Energy.NullEnergySessions != 2 {
		t.Errorf("unpriced %d null_energy %d, want 0 / 2", m.Spend.UnpricedSessions, m.Energy.NullEnergySessions)
	}
	for _, r := range m.Receipts {
		if r.CostSource != SourceUnknown || r.UnpricedSessions != 0 || hasFlag(r.Flags, FlagUnpriced) {
			t.Errorf("crumb-only receipt = source %s unpriced %d flags %v", r.CostSource, r.UnpricedSessions, r.Flags)
		}
	}
}

// A9: the place recorded with a charge wins over the current home, so
// moving house doesn't turn past home charges into "needs a price".
func TestBuild_PlaceSnapshotWins(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	// The user has since moved: the old home is ~645 km from the saved one.
	oldHome := at(priced(charge("old-home", "2026-10-02T03:00:00Z", 8*time.Hour, 60, 7.5), 0.11), awayLat, awayLon)
	oldHome.CostSource, oldHome.Place = SourceHomeRate, "home"
	legacy := at(priced(charge("legacy", "2026-10-03T03:00:00Z", 8*time.Hour, 40, 7.5), 0.11), awayLat, awayLon)
	legacy.CostSource = SourceHomeRate // no snapshot: derived from the current home
	pinnedAway := priced(charge("pinned-away", "2026-10-04T03:00:00Z", 2*time.Hour, 10, 7.5), 0.11)
	pinnedAway.Place = "away" // recorded away, though it's at today's home

	m := Build(Input{Charges: []Charge{oldHome, legacy, pinnedAway}, Config: cfg(), Now: ts("2026-10-20T00:00:00Z")}, month, true)
	if m.Spend.BySource[SourceHomeRate] != round4(60*0.11) || m.Spend.UnpricedSessions != 2 {
		t.Errorf("spend %+v: want only the snapshot-home session priced", m.Spend)
	}
	for _, r := range m.Receipts {
		for _, sess := range r.Sessions {
			switch sess.ChargeID {
			case "old-home":
				if sess.Place != "home" || sess.CostSource != SourceHomeRate || r.KMFromHome != nil {
					t.Errorf("old-home = place %s source %s km %v", sess.Place, sess.CostSource, r.KMFromHome)
				}
			case "legacy":
				if sess.Place != "away" || sess.CostSource != SourceUnknown {
					t.Errorf("legacy = place %s source %s", sess.Place, sess.CostSource)
				}
			case "pinned-away":
				if sess.Place != "away" || sess.CostSource != SourceUnknown || r.KMFromHome != nil {
					t.Errorf("pinned-away = place %s source %s km %v", sess.Place, sess.CostSource, r.KMFromHome)
				}
			}
		}
	}
}

// A9 / DA: sessions under CrumbKWh are crumbs, not "needs a price".
func TestBuild_CrumbFloor(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	tiny := charge("tiny", "2026-10-01T00:43:00Z", 30*time.Second, 4.7e-7, 7)
	small := charge("small", "2026-10-02T00:00:00Z", time.Minute, 0.04, 7)
	enough := charge("real", "2026-10-03T00:00:00Z", 5*time.Minute, 0.06, 7)
	m := Build(Input{Charges: []Charge{tiny, small, enough}, Config: cfg(), Now: ts("2026-10-20T00:00:00Z")}, month, false)
	if m.Spend.UnpricedSessions != 1 || m.Energy.NullEnergySessions != 2 {
		t.Errorf("unpriced %d null_energy %d, want 1 / 2", m.Spend.UnpricedSessions, m.Energy.NullEnergySessions)
	}
}

// D2 F6: under MinMilesForCostPerMile the ratio is noise, so it's null.
func TestBuild_CostPerMileNeedsDistance(t *testing.T) {
	month, _ := ParsePeriod("2026-10", time.UTC)
	cs := []Charge{priced(charge("c", "2026-10-02T00:00:00Z", time.Hour, 60, 7), 0.11)}
	drive := func(mi float64) []Drive {
		return []Drive{{ID: "d", VehicleID: "veh-1", StartedAt: ts("2026-10-03T10:00:00Z"),
			EndedAt: ts("2026-10-03T11:00:00Z"), DistanceMi: f(mi), Source: "live"}}
	}
	short := Build(Input{Charges: cs, Drives: drive(5.75), Config: cfg(), Now: ts("2026-10-04T00:00:00Z")}, month, false)
	if short.Driving.CostPerMile != nil || short.Driving.Coverage == nil {
		t.Errorf("5.75 mi: cost_per_mile %v (want null), coverage %v (still reported)", short.Driving.CostPerMile, short.Driving.Coverage)
	}
	long := Build(Input{Charges: cs, Drives: drive(150), Config: cfg(), Now: ts("2026-10-04T00:00:00Z")}, month, false)
	if long.Driving.CostPerMile == nil || *long.Driving.CostPerMile != round4(60*0.11/150) {
		t.Errorf("150 mi: cost_per_mile %v", long.Driving.CostPerMile)
	}
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

// A10: other surfaces get the Money tab's classification for one charge
// from the store's zero-for-NULL shape.
func TestClassifyFromCharge(t *testing.T) {
	start := ts("2026-10-09T02:00:00Z")
	awayL1 := charges.Charge{ID: "l1", VehicleID: "veh-1", StartedAt: start, EndedAt: start.Add(10 * time.Hour),
		EnergyAddedKWh: 20, MaxPowerKW: 1.4, Cost: 2.2, PricePerKWh: 0.11, Lat: awayLat, Lon: awayLon, Source: "live"}
	c := Classify(FromCharge(awayL1), cfg())
	if c.CostSource != SourceUnknown || c.Kind != "ac" || c.Place != "away" || c.CountsInSpend ||
		!hasFlag(c.Flags, FlagUnpriced) || !hasFlag(c.Flags, FlagHomeRateAway) {
		t.Errorf("legacy away L1 = %+v", c)
	}
	glitch := charges.Charge{ID: "g", VehicleID: "veh-1", StartedAt: start, EndedAt: start.Add(15 * time.Hour),
		EnergyAddedKWh: 66, MaxPowerKW: 145, Cost: 7.26, PricePerKWh: 0.11, Lat: homeLat, Lon: homeLon, Source: "live"}
	if c := Classify(FromCharge(glitch), cfg()); c.Kind != "ac" || c.CostSource != SourceHomeRate || !c.CountsInSpend {
		t.Errorf("glitched-peak home session = %+v", c)
	}
	free := charges.Charge{ID: "f", VehicleID: "veh-1", StartedAt: start, EndedAt: start.Add(time.Hour),
		EnergyAddedKWh: 10, MaxPowerKW: 7, CostSource: "user", Lat: awayLat, Lon: awayLon, Source: "live"}
	if c := Classify(FromCharge(free), cfg()); c.CostSource != SourceUser || !hasFlag(c.Flags, FlagFree) || !c.CountsInSpend {
		t.Errorf("Free session = %+v", c)
	}
	unpricedCrumb := charges.Charge{ID: "x", VehicleID: "veh-1", StartedAt: start, EndedAt: start.Add(time.Minute), Source: "live"}
	if c := Classify(FromCharge(unpricedCrumb), cfg()); hasFlag(c.Flags, FlagUnpriced) || !hasFlag(c.Flags, FlagNullEnergy) {
		t.Errorf("crumb = %+v", c)
	}
}

// F5: the list view applies the same dedupe as Money: an overlapping
// duplicate is flagged superseded and isn't counted in spend.
func TestClassifyAll_Superseded(t *testing.T) {
	a := priced(charge("live_a", "2026-05-08T22:54:46Z", 156*time.Minute, 18.5, 7.5), 0.11)
	b := priced(charge("live_b", "2026-05-08T22:54:49Z", 156*time.Minute, 18.1, 7.5), 0.11)
	b.CreatedAt = a.CreatedAt.Add(4 * time.Second)
	other := priced(charge("other", "2026-05-09T10:00:00Z", time.Hour, 10, 7.5), 0.11)
	got := ClassifyAll([]Charge{b, a, other}, cfg())
	if !got[1].CountsInSpend || hasFlag(got[1].Flags, FlagSuperseded) {
		t.Errorf("winner = %+v", got[1])
	}
	if got[0].CountsInSpend || !hasFlag(got[0].Flags, FlagSuperseded) {
		t.Errorf("duplicate = %+v, want superseded and not in spend", got[0])
	}
	if !got[2].CountsInSpend || hasFlag(got[2].Flags, FlagSuperseded) {
		t.Errorf("unrelated row = %+v", got[2])
	}
}
