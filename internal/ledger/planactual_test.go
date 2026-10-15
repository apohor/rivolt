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
			"ArrivalSoC": s.soc, "ChargeDurationSec": 1500})
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
		{"WaypointType":"charger","Name":"Electrify America - Somewhere","Latitude":1.6,"Longitude":2.6,"ChargeDurationSec":900},
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
	if c.InProgress || c.Miles < 500 || c.Miles > 700 { // there and back
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

func TestComparePlanOnTheReplayCorpus(t *testing.T) {
	base := sampleLedger(t)
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
	kwh := cmp.Stops[0].Actual.KWh
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
