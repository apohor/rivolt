package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// The replay corpus: the sample truck (rivian.SampleCorpus) run through
// the real recorder, as ledger rows. Its two road trips are the test
// trips: Pasadena → Las Vegas (fast charge at Barstow, Jan 23) and
// Pasadena → San Francisco (fast charge at Kettleman City, Feb 13).
var (
	sampleOnce sync.Once
	sampleIn   Input
)

func sampleLedger(t *testing.T) Input {
	t.Helper()
	sampleOnce.Do(func() {
		c := rivian.NewSampleCorpus(rivian.SampleSeed)
		sum := c.Summary()
		frames, err := c.Frames(context.Background(), sum.Start, sum.End.Add(time.Second))
		if err != nil {
			t.Fatalf("frames: %v", err)
		}
		r := rivian.NewReplayer("sample")
		r.FeedAll(frames)
		in := Input{Config: Config{Currency: "USD", HomeRate: 0.16,
			Home: &LatLon{rivian.SampleHome.Lat, rivian.SampleHome.Lon}}, Now: sum.End}
		for _, d := range r.Drives() {
			opt := func(v float64) *float64 {
				if v == 0 {
					return nil
				}
				return &v
			}
			in.Drives = append(in.Drives, Drive{ID: d.ID, VehicleID: d.VehicleID, StartedAt: d.StartedAt, EndedAt: d.EndedAt,
				DistanceMi: opt(d.DistanceMi), EnergyKWh: opt(d.EnergyUsedKWh), Source: d.Source,
				StartLat: opt(d.StartLat), StartLon: opt(d.StartLon), EndLat: opt(d.EndLat), EndLon: opt(d.EndLon),
				StartSoC: opt(d.StartSoCPct), EndSoC: opt(d.EndSoCPct)})
		}
		for _, ch := range r.Charges() {
			in.Charges = append(in.Charges, FromCharge(ch))
		}
		sampleIn = in
	})
	if len(sampleIn.Drives) == 0 {
		t.Fatal("replay produced no drives")
	}
	return sampleIn
}

var (
	pvaHome      = LatLon{34.1613, -118.1676}
	pvaVegas     = LatLon{36.1699, -115.1398}
	pvaSF        = LatLon{37.7955, -122.3937}
	pvaBarstow   = LatLon{34.851348, -117.082573} // RAN Barstow
	pvaBaker     = LatLon{35.266551, -116.072533}
	pvaKettleman = LatLon{35.986104, -119.957772}
)

type pvaStop struct {
	at             LatLon
	name, network  string
	kwh, rate, soc float64
}

// planJSONFor builds a saved trip's inputs and plan as the SPA stores
// them (rivian.TripPlan field names, costs with a per-stop breakdown).
func planJSONFor(t *testing.T, from, to LatLon, depart string, stops []pvaStop, cost float64, currency string) (json.RawMessage, json.RawMessage) {
	t.Helper()
	in := map[string]any{
		"origin":      map[string]any{"lat": from.Lat, "lon": from.Lon, "label": "Home"},
		"destination": map[string]any{"lat": to.Lat, "lon": to.Lon, "label": "There"},
		"extra_stops": []any{},
	}
	if depart != "" {
		in["departure_at"] = depart
	}
	wps := []map[string]any{{"WaypointType": "origin", "Latitude": from.Lat, "Longitude": from.Lon}}
	var breakdown []map[string]any
	for _, s := range stops {
		wps = append(wps, map[string]any{"WaypointType": "charger", "Name": s.name, "Latitude": s.at.Lat, "Longitude": s.at.Lon,
			"ArrivalSoC": s.soc, "DepartureSoC": s.soc + 30, "ChargeDurationSec": 1500})
		breakdown = append(breakdown, map[string]any{"network_name": s.network, "charger_name": s.name,
			"energy_kwh": s.kwh, "user_rate": s.rate})
	}
	wps = append(wps, map[string]any{"WaypointType": "destination", "Latitude": to.Lat, "Longitude": to.Lon})
	plan := map[string]any{
		"Routes": []any{map[string]any{"TotalTripDurationSec": 15000, "TotalDriveDistanceMeters": 270 * 1609.344,
			"ArrivalSoC": 25, "EnergyConsumptionKWh": 160, "Waypoints": wps}},
		"costs": []any{map[string]any{"currency": currency, "dcfc_spend_user_member": cost, "breakdown": breakdown}},
	}
	ib, _ := json.Marshal(in)
	pb, _ := json.Marshal(plan)
	return ib, pb
}

func vegasPlan(t *testing.T, depart string, stops ...pvaStop) Plan {
	t.Helper()
	if stops == nil {
		stops = []pvaStop{{pvaBarstow, "Barstow, CA", "Rivian Adventure Network", 33, 0.45, 55}}
	}
	in, pl := planJSONFor(t, pvaHome, pvaVegas, depart, stops, 14.85, "USD")
	p, err := ParsePlan(in, pl, time.Date(2026, 1, 20, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	return p
}

func TestParsePlan(t *testing.T) {
	p := vegasPlan(t, "2026-01-23T07:30:00Z")
	if p.Departure == nil || !p.Departure.Equal(time.Date(2026, 1, 23, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("departure = %v", p.Departure)
	}
	if p.Origin != pvaHome || p.Destination != pvaVegas || len(p.Stops) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	s := p.Stops[0]
	if s.Network != "Rivian Adventure Network" || *s.EstKWh != 33 || *s.EstRate != 0.45 || math.Abs(*s.EstCost-14.85) > 1e-9 {
		t.Errorf("stop = %+v", s)
	}
	if *p.Cost != 14.85 || p.Currency != "USD" || *p.EnergyKWh != 160 || *p.ArrivalSoC != 25 || *p.DurationMin != 250 ||
		math.Abs(*p.DistanceMi-270) > 1e-9 {
		t.Errorf("route figures = %+v", p)
	}

	// No departure in the inputs: the origin waypoint's time; a via
	// stop without a charge isn't a charger; a name alone finds the
	// network when there's no cost breakdown.
	plan := `{"Routes":[{"Waypoints":[
		{"WaypointType":"origin","Latitude":1,"Longitude":2,"DepartureTimeUTC":"2026-02-01T08:00:00Z"},
		{"WaypointType":"waypoint","Latitude":1.5,"Longitude":2.5},
		{"WaypointType":"charger","Name":"Electrify America - Somewhere","Latitude":1.6,"Longitude":2.6,"ArrivalSoC":20,"DepartureSoC":70,"ChargeDurationSec":900},
		{"WaypointType":"destination","Latitude":3,"Longitude":4}]}]}`
	p, err := ParsePlan(nil, json.RawMessage(plan), time.Now())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if p.Departure == nil || p.Departure.Hour() != 8 || len(p.Stops) != 1 || p.Stops[0].Network != "Electrify America" ||
		p.Origin != (LatLon{1, 2}) || p.Destination != (LatLon{3, 4}) || p.Cost != nil || p.EnergyKWh != nil {
		t.Errorf("from the route alone: %+v", p)
	}

	if _, err := ParsePlan(json.RawMessage(`{"extra_stops":[]}`), nil, time.Now()); !errors.Is(err, ErrPlanUnusable) {
		t.Errorf("no origin/destination: %v, want ErrPlanUnusable", err)
	}
	if _, err := ParsePlan(json.RawMessage(`{`), nil, time.Now()); err == nil {
		t.Error("bad JSON must fail")
	}
}

func TestMatchPlanOnTheReplayCorpus(t *testing.T) {
	in := sampleLedger(t)

	// The Vegas plan finds the Vegas outing, with high confidence.
	cands := MatchPlan(vegasPlan(t, "2026-01-23T07:30:00Z"), in)
	if len(cands) == 0 {
		t.Fatal("no candidates for the Vegas plan")
	}
	c := cands[0]
	day := c.StartedAt.Format("2006-01-02")
	if day != "2026-01-23" || c.Band != "high" || c.Confidence < 0.95 || !strings.HasPrefix(c.TripID, "trip_") {
		t.Errorf("Vegas candidate = %+v", c)
	}
	if c.InProgress || c.Miles < 250 || c.Miles > 300 { // the leg there, not the round trip
		t.Errorf("Vegas candidate miles %.1f, in progress %v", c.Miles, c.InProgress)
	}
	if c.Signals.Destination != 1 || c.Signals.Origin != 1 || c.Signals.RouteOverlap == nil || *c.Signals.RouteOverlap != 1 ||
		c.Signals.StartOffsetMin == nil || *c.Signals.StartOffsetMin != 10 {
		t.Errorf("signals = %+v", c.Signals)
	}
	if len(cands) > 1 && cands[1].Confidence >= planHighBand {
		t.Errorf("a second high-confidence candidate: %+v", cands[1])
	}

	// The SF plan finds the SF outing, not Vegas.
	in2, pl := planJSONFor(t, pvaHome, pvaSF, "2026-02-13T07:00:00Z",
		[]pvaStop{{pvaKettleman, "Bravo Farms - Tesla Supercharger", "Tesla Supercharger", 60, 0.50, 20}}, 30, "USD")
	sf, err := ParsePlan(in2, pl, time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	cands = MatchPlan(sf, in)
	if len(cands) == 0 || cands[0].StartedAt.Format("2006-01-02") != "2026-02-13" || cands[0].Band != "high" {
		t.Errorf("SF candidates = %+v", cands)
	}

	// No planned time: matched within 30 days of saving, time scores 0.5.
	cands = MatchPlan(vegasPlan(t, ""), in)
	if len(cands) == 0 || cands[0].StartedAt.Format("2006-01-02") != "2026-01-23" || cands[0].Signals.Time != 0.5 ||
		cands[0].Confidence != 0.88 {
		t.Errorf("undated plan = %+v", cands)
	}

	// The wrong day, or a destination never driven to: nothing.
	if cands := MatchPlan(vegasPlan(t, "2026-03-10T07:30:00Z"), in); len(cands) != 0 {
		t.Errorf("wrong day matched %+v", cands)
	}
	in3, pl3 := planJSONFor(t, pvaHome, LatLon{33.4484, -112.0740}, "2026-01-23T07:30:00Z", nil, 0, "USD") // Phoenix
	phx, _ := ParsePlan(in3, pl3, time.Now())
	if cands := MatchPlan(phx, in); len(cands) != 0 {
		t.Errorf("a destination never reached matched %+v", cands)
	}
}

// metric returns the named metric from a comparison.
func metric(t *testing.T, c PlanComparison, name string) PlanMetric {
	t.Helper()
	for _, m := range c.Metrics {
		if m.Metric == name {
			return m
		}
	}
	t.Fatalf("no %s metric", name)
	return PlanMetric{}
}

func hasReason(m PlanMetric, code string) *PlanReason {
	for i := range m.Reasons {
		if m.Reasons[i].Code == code {
			return &m.Reasons[i]
		}
	}
	return nil
}

func eq(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 0.006 }

// The Barstow fast charge, priced as the recorder would with a network
// estimate (the replay has no price lookup, so it's unpriced as is).
func pricedBarstow(in Input, rate float64) Input {
	out := in
	out.Charges = append([]Charge(nil), in.Charges...)
	for i, c := range out.Charges {
		if c.Lat != nil && haversineKM(*c.Lat, *c.Lon, pvaBarstow.Lat, pvaBarstow.Lon) < 0.25 {
			cost := rate * val(c.EnergyKWh)
			out.Charges[i].Cost, out.Charges[i].Currency = &cost, "USD"
			out.Charges[i].CostSource, out.Charges[i].Network = SourceNetworkEstimate, "Rivian Adventure Network"
		}
	}
	return out
}

// unpricedBarstow clears whatever price the replay recorded on the
// Barstow fast charge (the sample corpus bills it as Rivian since #144),
// so the comparison cases below control the price themselves.
func unpricedBarstow(in Input) Input {
	out := in
	out.Charges = append([]Charge(nil), in.Charges...)
	for i, c := range out.Charges {
		if c.Lat != nil && haversineKM(*c.Lat, *c.Lon, pvaBarstow.Lat, pvaBarstow.Lon) < 0.25 {
			out.Charges[i].Cost, out.Charges[i].Currency = nil, ""
			out.Charges[i].CostSource, out.Charges[i].Network = "", ""
		}
	}
	return out
}

// barstowKWh is the Barstow fast charge's stored energy, unrounded (the
// comparison's stop view rounds kWh to 0.1, the cost uses the raw value).
func barstowKWh(in Input) float64 {
	for _, c := range in.Charges {
		if c.Lat != nil && haversineKM(*c.Lat, *c.Lon, pvaBarstow.Lat, pvaBarstow.Lon) < 0.25 {
			return val(c.EnergyKWh)
		}
	}
	return 0
}

func TestComparePlanOnTheReplayCorpus(t *testing.T) {
	base := unpricedBarstow(sampleLedger(t))
	p := vegasPlan(t, "2026-01-23T07:30:00Z")
	trip := MatchPlan(p, base)[0].TripID

	if _, ok := ComparePlan(p, base, "trip_nope"); ok {
		t.Error("an unknown trip id must not compare")
	}

	// As replayed, the Barstow session is unpriced: no actual cost.
	cmp, ok := ComparePlan(p, base, trip)
	if !ok {
		t.Fatal("ComparePlan: not found")
	}
	cost := metric(t, cmp, "cost")
	if cost.Actual != nil || cost.Delta != nil || *cost.ActualComplete || *cost.UnpricedSessions != 1 ||
		!eq(cost.ActualKnown, 0) || !eq(cost.Planned, 14.85) || hasReason(cost, "unpriced") == nil {
		t.Errorf("unpriced cost = %+v", cost)
	}

	// Leg: home → Barstow → Vegas, the fast charge between; arrives at
	// 12:05 with 27%.
	if len(cmp.Leg.DriveIDs) != 2 || len(cmp.Leg.ChargeIDs) != 1 || cmp.Leg.ArrivedAt.Format("15:04") != "12:05" ||
		cmp.Leg.Miles < 250 || cmp.Leg.Miles > 300 || cmp.Status != "complete" {
		t.Errorf("leg = %+v, status %q", cmp.Leg, cmp.Status)
	}
	if soc := metric(t, cmp, "arrival_soc"); !eq(soc.Actual, 27) || !eq(soc.Delta, 2) {
		t.Errorf("arrival soc = %+v", soc)
	}
	if d := metric(t, cmp, "duration"); !eq(d.Actual, 265) || !eq(d.Delta, 15) {
		t.Errorf("duration = %+v", d)
	}
	if e := metric(t, cmp, "energy_kwh"); e.Actual == nil || *e.Actual <= 0 || !eq(e.Planned, 160) {
		t.Errorf("energy = %+v", e)
	}
	if s := metric(t, cmp, "dcfc_stops"); !eq(s.Planned, 1) || !eq(s.Actual, 1) || !eq(s.Delta, 0) {
		t.Errorf("stops metric = %+v", s)
	}
	if len(cmp.Stops) != 1 || cmp.Stops[0].Match != "same_station" || cmp.Stops[0].Actual.Kind != "dcfc" ||
		cmp.Stops[0].Actual.Cost != nil || cmp.Stops[0].Actual.CostSource != SourceUnknown {
		t.Errorf("stops = %+v", cmp.Stops)
	}

	// Priced at $0.40/kWh estimated vs $0.45 planned: complete, the
	// rate difference explains the delta, flagged as an estimate.
	in := pricedBarstow(base, 0.40)
	cmp, _ = ComparePlan(p, in, trip)
	cost = metric(t, cmp, "cost")
	kwh := barstowKWh(in)
	if !*cost.ActualComplete || !eq(cost.Actual, 0.40*kwh) || !eq(cost.Delta, 0.40*kwh-14.85) ||
		!eq(ptrRound2(cost.ActualSources[SourceNetworkEstimate]), 0.40*kwh) {
		t.Errorf("priced cost = %+v (kWh %.1f)", cost, kwh)
	}
	r := hasReason(cost, "rate_differed")
	if r == nil || !eq(r.Amount, -0.05*kwh) || !eq(r.Rate, 0.40) || !eq(r.PlanRate, 0.45) || !r.Estimated ||
		r.Network != "Rivian Adventure Network" ||
		r.Detail != "Rivian Adventure Network's price is estimated at $0.40/kWh; the plan assumed $0.45." {
		t.Errorf("rate reason = %+v", r)
	}
	if hasReason(cost, "estimated") == nil || hasReason(cost, "unpriced") != nil {
		t.Errorf("reasons = %+v", cost.Reasons)
	}
	if cost.Reasons[0].Code != "rate_differed" {
		t.Errorf("reasons must lead with the largest amount: %+v", cost.Reasons)
	}

	// A planned stop at Baker that never happened: skipped, worth its
	// planned cost.
	p2 := vegasPlan(t, "2026-01-23T07:30:00Z",
		pvaStop{pvaBarstow, "Barstow, CA", "Rivian Adventure Network", 33, 0.45, 55},
		pvaStop{pvaBaker, "Baker", "EVgo", 10, 0.50, 40})
	cmp, _ = ComparePlan(p2, in, trip)
	if len(cmp.Stops) != 2 || cmp.Stops[1].Match != "skipped" {
		t.Errorf("stops = %+v", cmp.Stops)
	}
	if r := hasReason(metric(t, cmp, "cost"), "skipped_stop"); r == nil || !eq(r.Amount, -5) ||
		r.Detail != "You skipped a planned stop, saving about $5.00." || r.Stop != "EVgo" {
		t.Errorf("skipped reason = %+v", r)
	}
	if s := metric(t, cmp, "dcfc_stops"); !eq(s.Delta, -1) || hasReason(s, "skipped_stop") == nil {
		t.Errorf("stops metric = %+v", s)
	}

	// A plan with no stops: the actual fast charge is an extra.
	p3 := vegasPlan(t, "2026-01-23T07:30:00Z", []pvaStop{}...)
	cmp, _ = ComparePlan(p3, in, trip)
	if len(cmp.Stops) != 1 || cmp.Stops[0].Match != "extra" {
		t.Errorf("stops = %+v", cmp.Stops)
	}
	if r := hasReason(metric(t, cmp, "cost"), "extra_stop"); r == nil || !eq(r.Amount, 0.40*kwh) ||
		r.Network != "Rivian Adventure Network" || r.Detail != fmt.Sprintf("You made an extra fast-charge stop, which cost $%.2f.", 0.40*kwh) {
		t.Errorf("extra reason = %+v", r)
	}

	// Planned in another currency: no cost comparison at all.
	ib, pb := planJSONFor(t, pvaHome, pvaVegas, "2026-01-23T07:30:00Z", nil, 14, "EUR")
	pe, _ := ParsePlan(ib, pb, time.Now())
	if c := metric(t, mustCompare(t, pe, in, trip), "cost"); c.Planned != nil || c.Actual != nil || hasReason(c, "currency_mismatch") == nil {
		t.Errorf("currency mismatch = %+v", c)
	}
}

func mustCompare(t *testing.T, p Plan, in Input, trip string) PlanComparison {
	t.Helper()
	c, ok := ComparePlan(p, in, trip)
	if !ok {
		t.Fatal("ComparePlan: not found")
	}
	return c
}

// The JSON is the contract's shape (§4, §5): backend marshals as is.
func TestPlanJSONShape(t *testing.T) {
	in := pricedBarstow(sampleLedger(t), 0.40)
	p := vegasPlan(t, "2026-01-23T07:30:00Z")
	cands := MatchPlan(p, in)
	cmp := mustCompare(t, p, in, cands[0].TripID)
	cmp.PlanFrom = "snapshot"
	b, _ := json.Marshal(map[string]any{"candidates": cands, "comparison": cmp})
	s := string(b)
	for _, k := range []string{`"trip_id":"trip_`, `"band":"high"`, `"route_overlap":1`, `"start_offset_min":10`,
		`"plan_from":"snapshot"`, `"drive_ids":[`, `"metric":"cost"`, `"actual_complete":true`, `"unpriced_sessions":0`,
		`"actual_sources":{`, `"metric":"arrival_soc"`, `"match":"same_station"`, `"cost_source":"network_estimate"`,
		`"est_cost":14.85`, `"reasons":[`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %s in\n%s", k, s)
		}
	}
	for _, k := range []string{`"status":"complete"`, `"miles":`, `"in_progress":false`, `"plan_rate":0.45`, `"estimated":true`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %s", k)
		}
	}
	if strings.Contains(s, `"actual_complete":null`) || strings.Contains(s, `"reasons":null`) {
		t.Errorf("null where the contract has a value:\n%s", s)
	}
}

// A trip still on the way: the ledger as it stood at Barstow, before
// reaching Vegas. It's a candidate (medium at most, in progress) and
// the comparison says so, with no arrival figures yet.
func TestPlanInProgress(t *testing.T) {
	full := sampleLedger(t)
	cut := time.Date(2026, 1, 23, 9, 40, 0, 0, time.UTC) // charged at Barstow, back on the road
	in := Input{Config: full.Config, Now: cut}
	for _, d := range full.Drives {
		if d.StartedAt.Before(cut) && d.EndedAt.Before(cut) {
			in.Drives = append(in.Drives, d)
		}
	}
	for _, c := range full.Charges {
		if c.EndedAt.Before(cut) {
			in.Charges = append(in.Charges, c)
		}
	}
	p := vegasPlan(t, "2026-01-23T07:30:00Z")
	cands := MatchPlan(p, in)
	if len(cands) == 0 || !cands[0].InProgress || cands[0].Band != "medium" || cands[0].StartedAt.Format("2006-01-02") != "2026-01-23" {
		t.Fatalf("in-progress candidates = %+v", cands)
	}
	cmp := mustCompare(t, p, in, cands[0].TripID)
	if cmp.Status != "in_progress" || metric(t, cmp, "arrival_soc").Actual != nil || metric(t, cmp, "duration").Actual != nil {
		t.Errorf("in-progress comparison: status %q, metrics %+v", cmp.Status, cmp.Metrics)
	}
	if s := metric(t, cmp, "dcfc_stops"); !eq(s.Actual, 1) {
		t.Errorf("stops so far = %+v", s)
	}

	// Days later with nothing more recorded it isn't live any more, and
	// a destination never reached isn't a match.
	in.Now = cut.Add(48 * time.Hour)
	if cands := MatchPlan(p, in); len(cands) != 0 {
		t.Errorf("a stale, unfinished outing matched: %+v", cands)
	}
}

// Breakdown entries are matched to stops by charger name, and stops
// are exactly the waypoints tripadvice prices: an OTHER point, a via
// WAYPOINT (even one with a charge duration) and a charger the car
// leaves no fuller are skipped, so they can't shift a neighbour's rate.
func TestParsePlanStopsMatchTheBreakdown(t *testing.T) {
	plan := `{"Routes":[{"Waypoints":[
		{"WaypointType":"origin","Latitude":34.16,"Longitude":-118.17},
		{"WaypointType":"OTHER","Name":"Scenic overlook","Latitude":34.5,"Longitude":-117.5,"ArrivalSoC":60,"DepartureSoC":70},
		{"WaypointType":"charger","Name":"Barstow, CA","Latitude":34.85,"Longitude":-117.08,"ArrivalSoC":55,"DepartureSoC":85,"ChargeDurationSec":1500},
		{"WaypointType":"waypoint","Name":"Friend's place","Latitude":35.0,"Longitude":-116.5,"ArrivalSoC":70,"DepartureSoC":75,"ChargeDurationSec":600},
		{"WaypointType":"charger","Name":"Baker EVgo","Latitude":35.27,"Longitude":-116.07,"ArrivalSoC":60,"DepartureSoC":60,"ChargeDurationSec":0},
		{"WaypointType":"charger","Name":"Prizm Outlets (Primm, NV)","Latitude":35.61,"Longitude":-115.39,"ArrivalSoC":40,"DepartureSoC":70,"ChargeDurationSec":1200},
		{"WaypointType":"destination","Latitude":36.17,"Longitude":-115.14}]}],
	 "costs":[{"currency":"USD","dcfc_spend_user_member":30,"breakdown":[
		{"network_name":"Rivian Adventure Network","charger_name":"Barstow, CA","energy_kwh":40,"user_rate":0.45},
		{"network_name":"Electrify America","charger_name":"Prizm Outlets (Primm, NV)","energy_kwh":38,"user_rate":0.56}]}]}`
	p, err := ParsePlan(nil, json.RawMessage(plan), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Stops) != 2 || p.Stops[0].Name != "Barstow, CA" || p.Stops[1].Name != "Prizm Outlets (Primm, NV)" {
		t.Fatalf("stops = %+v", p.Stops)
	}
	if p.Stops[0].Network != "Rivian Adventure Network" || *p.Stops[0].EstRate != 0.45 || *p.Stops[0].EstKWh != 40 ||
		p.Stops[1].Network != "Electrify America" || *p.Stops[1].EstRate != 0.56 || *p.Stops[1].EstKWh != 38 {
		t.Errorf("breakdown on the wrong stops: %+v / %+v", p.Stops[0], p.Stops[1])
	}

	// A breakdown that names a charger the stops don't have leaves that
	// stop without a rate rather than borrowing another's.
	plan = strings.Replace(plan, `"charger_name":"Barstow, CA"`, `"charger_name":"Somewhere else"`, 1)
	p, _ = ParsePlan(nil, json.RawMessage(plan), time.Now())
	if p.Stops[0].EstRate != nil || p.Stops[1].EstRate == nil || *p.Stops[1].EstRate != 0.56 {
		t.Errorf("unmatched breakdown entry: %+v / %+v", p.Stops[0], p.Stops[1])
	}
}

// A plan that doesn't start at home, the drive back from Las Vegas, is
// matched and compared on that leg only, not the round trip that began
// two days earlier.
func TestPlanReturnLeg(t *testing.T) {
	in := pricedBarstow(sampleLedger(t), 0.40)
	ib, pb := planJSONFor(t, pvaVegas, pvaHome, "2026-01-25T09:00:00Z",
		[]pvaStop{{LatLon{35.606006, -115.389080}, "Prizm Outlets (Primm, NV)", "Electrify America", 40, 0.56, 30}}, 22.4, "USD")
	p, err := ParsePlan(ib, pb, time.Date(2026, 1, 24, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	cands := MatchPlan(p, in)
	if len(cands) == 0 {
		t.Fatal("no candidates for the return leg")
	}
	c := cands[0]
	if c.StartedAt.Format("2006-01-02 15") != "2026-01-25 09" || c.Band != "high" || c.Signals.Origin != 1 ||
		c.Signals.StartOffsetMin == nil || math.Abs(*c.Signals.StartOffsetMin) > 5 || c.Miles > 300 {
		t.Errorf("return-leg candidate = %+v", c)
	}
	cmp := mustCompare(t, p, in, c.TripID)
	if len(cmp.Leg.DriveIDs) != 2 || len(cmp.Leg.ChargeIDs) != 1 || cmp.Status != "complete" {
		t.Errorf("leg = %+v (status %s), want Vegas → Primm → home only", cmp.Leg, cmp.Status)
	}
	for _, s := range cmp.Stops {
		if s.Actual != nil && strings.Contains(s.Actual.Network, "Rivian") {
			t.Errorf("the outbound Barstow charge is on the return leg: %+v", s)
		}
	}
	if s := metric(t, cmp, "dcfc_stops"); !eq(s.Actual, 1) || !eq(s.Delta, 0) {
		t.Errorf("stops = %+v", s)
	}
	if d := metric(t, cmp, "duration"); d.Actual == nil || *d.Actual > 6*60 {
		t.Errorf("duration = %+v, want the drive back, not days", d)
	}
}

// windowed is `in` cut to what Load would return for PlanWindow(p).
func windowed(in Input, p Plan) Input {
	from, to := PlanWindow(p)
	out := Input{Config: in.Config, Now: in.Now}
	for _, d := range in.Drives {
		if d.StartedAt.Before(to) && !d.EndedAt.Before(from) {
			out.Drives = append(out.Drives, d)
		}
	}
	for _, c := range in.Charges {
		if c.StartedAt.Before(to) && !c.EndedAt.Before(from) {
			out.Charges = append(out.Charges, c)
		}
	}
	return out
}

// The ledger loaded over PlanWindow holds the whole outing even when a
// return-leg plan starts days after the outing left home, so the trip
// id is the outing's own (as /api/ledger/trips names it), not one named
// after the first drive that happened to be loaded.
func TestPlanWindowHoldsTheWholeOuting(t *testing.T) {
	full := sampleLedger(t)
	ib, pb := planJSONFor(t, pvaSF, pvaHome, "2026-02-16T13:00:00Z",
		[]pvaStop{{pvaKettleman, "Bravo Farms - Tesla Supercharger", "Tesla Supercharger", 60, 0.50, 20}}, 30, "USD")
	p, err := ParsePlan(ib, pb, time.Date(2026, 2, 15, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	all := MatchPlan(p, full)
	if len(all) == 0 {
		t.Fatal("no candidate on the full ledger")
	}
	got := MatchPlan(p, windowed(full, p))
	if len(got) == 0 || got[0].TripID != all[0].TripID {
		t.Errorf("windowed candidate %+v, want trip id %s (the outing left %s)", got, all[0].TripID, all[0].TripID)
	}
	if got[0].StartedAt.Format("2006-01-02") != "2026-02-16" {
		t.Errorf("the leg starts %s, want the drive back on Feb 16", got[0].StartedAt)
	}
}

// A coffee stop a few km from home doesn't cut the leg: it starts at the
// last drive that leaves from the plan's origin itself, falling back to
// one within 25 km only when none does.
func TestPlanLegStartsAtTheOrigin(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	t0 := time.Date(2026, 3, 2, 7, 0, 0, 0, time.UTC)
	cafe := LatLon{34.1950, -118.1380} // ~4.6 km from home
	drive := func(id string, from, to LatLon, startMin, endMin int, mi float64) Drive {
		return Drive{ID: id, VehicleID: "rv", StartedAt: t0.Add(time.Duration(startMin) * time.Minute),
			EndedAt: t0.Add(time.Duration(endMin) * time.Minute), DistanceMi: f(mi), EnergyKWh: f(mi / 2.5),
			StartLat: f(from.Lat), StartLon: f(from.Lon), EndLat: f(to.Lat), EndLon: f(to.Lon), StartSoC: f(80), EndSoC: f(40)}
	}
	in := Input{Config: Config{Currency: "USD", Home: &pvaHome}, Now: t0.Add(48 * time.Hour),
		Drives: []Drive{
			drive("d-cafe", pvaHome, cafe, 0, 10, 3),
			drive("d-barstow", cafe, pvaBarstow, 30, 130, 90),
			drive("d-vegas", pvaBarstow, pvaVegas, 160, 300, 160),
		}}
	ib, pb := planJSONFor(t, pvaHome, pvaVegas, "2026-03-02T07:00:00Z", nil, 0, "USD")
	p, _ := ParsePlan(ib, pb, t0.Add(-24*time.Hour))
	cands := MatchPlan(p, in)
	if len(cands) == 0 || !cands[0].StartedAt.Equal(t0) {
		t.Fatalf("candidates = %+v, want the leg from home at 07:00", cands)
	}
	cmp := mustCompare(t, p, in, cands[0].TripID)
	if len(cmp.Leg.DriveIDs) != 3 || cmp.Leg.DriveIDs[0] != "d-cafe" {
		t.Errorf("leg drives = %v, want all three from home", cmp.Leg.DriveIDs)
	}

	// Planned from the café itself: the leg starts there.
	ib, pb = planJSONFor(t, cafe, pvaVegas, "2026-03-02T07:30:00Z", nil, 0, "USD")
	p, _ = ParsePlan(ib, pb, t0.Add(-24*time.Hour))
	cmp = mustCompare(t, p, in, MatchPlan(p, in)[0].TripID)
	if len(cmp.Leg.DriveIDs) != 2 || cmp.Leg.DriveIDs[0] != "d-barstow" {
		t.Errorf("leg from the café = %v, want d-barstow, d-vegas", cmp.Leg.DriveIDs)
	}
}
