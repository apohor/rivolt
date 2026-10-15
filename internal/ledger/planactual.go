package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// Plan vs Actual (plan-vs-actual-contract.md v1): link a saved trip plan
// to the outing that carried it out and compare the two. Everything on
// the actual side comes from the same effective rows, classification and
// cost sources as Money and /api/ledger/trips; nothing is re-derived.

// Matching and pairing thresholds (contract §2, §5).
const (
	planNearKM      = 2.0  // a signal scores 1 within this
	planFarKM       = 25.0 // and 0 beyond; also the destination gate
	planStopNearKM  = 5.0  // route overlap and stop pairing
	planSameStation = 0.25 // km: the same charger
	planHighBand    = 0.80
	planMediumBand  = 0.50
	planMaxCands    = 3
)

// Plan is a saved trip plan, parsed from saved_trips.inputs and .plan
// (or a link-time snapshot of them).
type Plan struct {
	CreatedAt   time.Time
	Departure   *time.Time // planned departure; nil when not set
	Origin      LatLon
	Destination LatLon
	Stops       []PlannedStop // charging stops, in route order
	EnergyKWh   *float64      // route energy
	DistanceMi  *float64
	DurationMin *float64
	ArrivalSoC  *float64
	Cost        *float64 // DC fast spend at the user's own member rates
	Currency    string
}

// PlannedStop is one charging stop on the planned route.
type PlannedStop struct {
	Name       string
	Network    string // display name; "" when unknown
	Lat, Lon   float64
	ArrivalSoC *float64
	ChargeMin  *float64
	EstKWh     *float64 // from the plan's cost breakdown
	EstRate    *float64 // the user's rate there, per kWh
	EstCost    *float64
}

// planInputs is the part of saved_trips.inputs matching needs.
type planInputs struct {
	Origin      *struct{ Lat, Lon float64 } `json:"origin"`
	Destination *struct{ Lat, Lon float64 } `json:"destination"`
	DepartureAt string                      `json:"departure_at"`
}

// planJSON is the part of saved_trips.plan (rivian.TripPlan as the SPA
// stores it, plus its costs) the comparison needs.
type planJSON struct {
	Routes []struct {
		TotalTripDurationSec     float64
		TotalDriveDistanceMeters float64
		ArrivalSoC               float64
		EnergyConsumptionKWh     float64
		Waypoints                []struct {
			WaypointType      string
			Name              string
			Latitude          float64
			Longitude         float64
			ArrivalSoC        float64
			DepartureSoC      float64
			ChargeDurationSec float64
			DepartureTimeUTC  string
		}
	}
	Costs []struct {
		Currency            string   `json:"currency"`
		DCFCSpendUserMember *float64 `json:"dcfc_spend_user_member"`
		Breakdown           []struct {
			NetworkName string  `json:"network_name"`
			ChargerName string  `json:"charger_name"`
			EnergyKWh   float64 `json:"energy_kwh"`
			UserRate    float64 `json:"user_rate"`
		} `json:"breakdown"`
	} `json:"costs"`
}

// ErrPlanUnusable is returned by ParsePlan when the saved trip has
// neither an origin and destination in its inputs nor a planned route
// to take them from.
var ErrPlanUnusable = errors.New("ledger: saved trip has no origin and destination")

// ParsePlan reads a saved trip's inputs and plan JSON. The plan may be
// empty (saved before planning): matching still works from the inputs,
// and the comparison has no planned figures.
func ParsePlan(inputs, plan json.RawMessage, createdAt time.Time) (Plan, error) {
	p := Plan{CreatedAt: createdAt.UTC()}
	var in planInputs
	if len(inputs) > 0 {
		if err := json.Unmarshal(inputs, &in); err != nil {
			return p, fmt.Errorf("ledger: plan inputs: %w", err)
		}
	}
	var pj planJSON
	if len(plan) > 0 && string(plan) != "null" {
		if err := json.Unmarshal(plan, &pj); err != nil {
			return p, fmt.Errorf("ledger: plan: %w", err)
		}
	}
	haveO, haveD := in.Origin != nil, in.Destination != nil
	if haveO {
		p.Origin = LatLon{in.Origin.Lat, in.Origin.Lon}
	}
	if haveD {
		p.Destination = LatLon{in.Destination.Lat, in.Destination.Lon}
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(in.DepartureAt)); err == nil {
		t = t.UTC()
		p.Departure = &t
	}
	if len(pj.Routes) > 0 {
		r := pj.Routes[0] // v1: the first (recommended) route
		p.EnergyKWh = posPtr(r.EnergyConsumptionKWh)
		p.DistanceMi = posPtr(r.TotalDriveDistanceMeters / 1609.344)
		p.DurationMin = posPtr(r.TotalTripDurationSec / 60)
		if r.ArrivalSoC > 0 || r.EnergyConsumptionKWh > 0 {
			v := r.ArrivalSoC
			p.ArrivalSoC = &v
		}
		for _, w := range r.Waypoints {
			switch strings.ToLower(strings.TrimSpace(w.WaypointType)) {
			case "origin":
				if !haveO {
					p.Origin, haveO = LatLon{w.Latitude, w.Longitude}, true
				}
				if p.Departure == nil {
					if t, err := time.Parse(time.RFC3339, strings.TrimSpace(w.DepartureTimeUTC)); err == nil {
						t = t.UTC()
						p.Departure = &t
					}
				}
				continue
			case "destination":
				if !haveD {
					p.Destination, haveD = LatLon{w.Latitude, w.Longitude}, true
				}
				continue
			case "waypoint", "other":
				continue // a user via-stop or a non-charger point
			}
			// Exactly tripadvice.EstimateRoute's filter, so the stops are
			// the ones its cost breakdown priced: a charger the car
			// leaves fuller than it arrived.
			if w.DepartureSoC-w.ArrivalSoC <= 0 {
				continue
			}
			s := PlannedStop{Name: w.Name, Lat: w.Latitude, Lon: w.Longitude,
				ChargeMin: posPtr(w.ChargeDurationSec / 60)}
			if w.ArrivalSoC > 0 {
				v := w.ArrivalSoC
				s.ArrivalSoC = &v
			}
			p.Stops = append(p.Stops, s)
		}
	}
	if len(pj.Costs) > 0 {
		c := pj.Costs[0]
		p.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
		if c.DCFCSpendUserMember != nil {
			v := *c.DCFCSpendUserMember
			p.Cost = &v
		}
		// Pair breakdown entries with stops by charger name, in route
		// order (each entry used once); a stop without one has no
		// planned rate rather than a neighbour's.
		used := make([]bool, len(c.Breakdown))
		for i := range p.Stops {
			k := -1
			for j, b := range c.Breakdown {
				if !used[j] && strings.EqualFold(strings.TrimSpace(b.ChargerName), strings.TrimSpace(p.Stops[i].Name)) {
					k = j
					break
				}
			}
			if k < 0 {
				continue
			}
			used[k] = true
			b := c.Breakdown[k]
			p.Stops[i].Network = strings.TrimSpace(b.NetworkName)
			if b.EnergyKWh > 0 {
				kwh := b.EnergyKWh
				p.Stops[i].EstKWh = &kwh
				if b.UserRate > 0 {
					rate, cost := b.UserRate, b.UserRate*b.EnergyKWh
					p.Stops[i].EstRate, p.Stops[i].EstCost = &rate, &cost
				}
			}
		}
	}
	for i := range p.Stops {
		if p.Stops[i].Network == "" {
			if n := dcfcrates.MatchNetwork(p.Stops[i].Name); n.Slug != dcfcrates.UnmatchedNetwork.Slug {
				p.Stops[i].Network = n.DisplayName
			}
		}
	}
	if !haveO || !haveD {
		return p, ErrPlanUnusable
	}
	return p, nil
}

func posPtr(v float64) *float64 {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// candidateWindow is when a matching outing may start (contract §2).
func (p Plan) candidateWindow() (time.Time, time.Time) {
	if p.Departure != nil {
		return p.Departure.Add(-6 * time.Hour), p.Departure.Add(24 * time.Hour)
	}
	return p.CreatedAt.Add(-time.Hour), p.CreatedAt.Add(30 * 24 * time.Hour)
}

// PlanWindow is the [from, to) to Load the ledger Input over for p:
// the candidate window padded by the longest an outing can stay away on
// both sides, so every outing whose leg starts in the window is loaded
// whole. A partly loaded outing would be named after its first loaded
// drive, a trip_id that doesn't match /api/ledger/trips and moves with
// the window (a return-leg plan's outing can have left days earlier).
func PlanWindow(p Plan) (time.Time, time.Time) {
	from, to := p.candidateWindow()
	return from.Add(-TripMaxParkedAway), to.Add(TripMaxParkedAway)
}

// PlanCandidate is one outing that may have carried out the plan (§4).
type PlanCandidate struct {
	TripID     string    `json:"trip_id"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Miles      float64   `json:"miles"`
	Confidence float64   `json:"confidence"`
	Band       string    `json:"band"`
	// InProgress: the outing is still under way (not back home, last
	// activity within TripMaxParked) and hasn't reached the plan's
	// destination yet. Its band is at most medium.
	InProgress bool        `json:"in_progress"`
	Signals    PlanSignals `json:"signals"`
}

// PlanSignals are the per-signal scores (0..1) behind a confidence.
type PlanSignals struct {
	Destination    float64  `json:"destination"`
	Origin         float64  `json:"origin"`
	Time           float64  `json:"time"`
	RouteOverlap   *float64 `json:"route_overlap"`
	DestinationKM  float64  `json:"destination_km"`
	OriginKM       *float64 `json:"origin_km"`
	StartOffsetMin *float64 `json:"start_offset_min"`
}

// MatchPlan scores the outings in `in` against p and returns those with
// confidence ≥ 0.50, best first (ties: earlier start), at most 3.
func MatchPlan(p Plan, in Input) []PlanCandidate {
	wFrom, wTo := p.candidateWindow()
	out := []PlanCandidate{}
	for _, o := range outingsOf(in) {
		if outingID(o) == "" {
			continue // no drive: charging at home between outings
		}
		sp := planSpan(p, o)
		leg := o[sp.from : sp.to+1]
		ds := outingDrives(leg)
		start := o[sp.from].start.UTC()
		if start.Before(wFrom) || !start.Before(wTo) {
			continue
		}
		destKM := math.Inf(1)
		for _, d := range ds {
			if km, ok := kmTo(d.EndLat, d.EndLon, p.Destination); ok && km < destKM {
				destKM = km
			}
		}
		live := outingLive(o, in)
		inProgress := live && destKM > planNearKM
		if destKM > planFarKM && !inProgress {
			continue // the destination gate (waived while still under way)
		}
		var sig PlanSignals
		if !math.IsInf(destKM, 1) {
			sig.DestinationKM = round2(destKM)
		}
		sig.Destination = proximity(destKM)
		if inProgress {
			sig.Destination = 0.5 // not there yet: neither for nor against
		}
		if km, ok := kmTo(ds[0].StartLat, ds[0].StartLon, p.Origin); ok {
			k := round2(km)
			sig.OriginKM, sig.Origin = &k, proximity(km)
		}
		sig.Time = 0.5
		if p.Departure != nil {
			off := start.Sub(*p.Departure)
			m := math.Round(off.Minutes())
			sig.StartOffsetMin = &m
			sig.Time = timeScore(off)
		}
		weights := 0.35*sig.Destination + 0.20*sig.Origin + 0.25*sig.Time
		total := 0.80
		if len(p.Stops) > 0 {
			ov := routeOverlap(p.Stops, leg)
			sig.RouteOverlap = &ov
			weights += 0.20 * ov
			total = 1.0
		}
		sig.Destination, sig.Origin, sig.Time = round2(sig.Destination), round2(sig.Origin), round2(sig.Time)
		conf := round2(weights / total)
		if conf < planMediumBand {
			continue
		}
		band := "medium"
		if conf >= planHighBand && !inProgress {
			band = "high"
		}
		end := o[sp.arrive].end.UTC()
		if inProgress {
			end = outingEnd(o)
		}
		out = append(out, PlanCandidate{TripID: outingID(o), StartedAt: start, EndedAt: end,
			Miles: drivenMiles(ds), Confidence: conf, Band: band, InProgress: inProgress, Signals: sig})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	if len(out) > planMaxCands {
		out = out[:planMaxCands]
	}
	return out
}

func proximity(km float64) float64 {
	switch {
	case km <= planNearKM:
		return 1
	case km >= planFarKM:
		return 0
	}
	return (planFarKM - km) / (planFarKM - planNearKM)
}

func timeScore(off time.Duration) float64 {
	h := math.Abs(off.Hours())
	switch {
	case h <= 1:
		return 1
	case h >= 24:
		return 0
	}
	return (24 - h) / 23
}

// routeOverlap is the share of planned stops with an actual charge, or
// a drive start or end, within planStopNearKM.
func routeOverlap(stops []PlannedStop, o []tripEvent) float64 {
	near := 0
	for _, s := range stops {
		at := LatLon{s.Lat, s.Lon}
		for _, e := range o {
			var hit bool
			switch {
			case e.charge != nil:
				hit = within(e.charge.Lat, e.charge.Lon, at, planStopNearKM)
			case e.drive != nil:
				hit = within(e.drive.StartLat, e.drive.StartLon, at, planStopNearKM) ||
					within(e.drive.EndLat, e.drive.EndLon, at, planStopNearKM)
			}
			if hit {
				near++
				break
			}
		}
	}
	return round2(float64(near) / float64(len(stops)))
}

func within(lat, lon *float64, at LatLon, km float64) bool {
	d, ok := kmTo(lat, lon, at)
	return ok && d <= km
}

// kmTo is the distance from a recorded position to at; false when the
// position is missing (or the 0,0 placeholder).
func kmTo(lat, lon *float64, at LatLon) (float64, bool) {
	if lat == nil || lon == nil || (*lat == 0 && *lon == 0) {
		return 0, false
	}
	return haversineKM(at.Lat, at.Lon, *lat, *lon), true
}

// outingLive reports whether the outing is still under way as of
// in.Now: a charge on it still open, or its last activity within
// TripMaxParked and its last known position away from home.
func outingLive(o []tripEvent, in Input) bool {
	if in.Now.IsZero() {
		return false
	}
	for _, e := range o {
		if e.charge != nil && isOpen(e.charge.FinalState) {
			return true
		}
	}
	if in.Now.Sub(outingEnd(o)) >= TripMaxParked {
		return false
	}
	return lastAway(o, in.Config.Home)
}

func drivenMiles(ds []*Drive) float64 {
	mi := 0.0
	for _, d := range ds {
		mi += val(d.DistanceMi)
	}
	return round1(mi)
}

func outingDrives(o []tripEvent) []*Drive {
	var ds []*Drive
	for _, e := range o {
		if e.drive != nil {
			ds = append(ds, e.drive)
		}
	}
	return ds
}

// outingID is the outing's id, as /api/ledger/trips names trips.
func outingID(o []tripEvent) string {
	for _, e := range o {
		if e.drive != nil {
			return "trip_" + e.drive.ID
		}
	}
	return ""
}

func outingEnd(o []tripEvent) time.Time {
	var end time.Time
	for _, e := range o {
		if e.end.After(end) {
			end = e.end
		}
	}
	return end.UTC()
}

// PlanComparison is the planned-vs-actual comparison of one linked (or
// suggested) outing (contract §5). PlanFrom is set by the caller
// ("snapshot" for a link, "current" for a suggestion).
type PlanComparison struct {
	// Status is "in_progress" while the outing hasn't reached the
	// plan's destination or is still under way (not home yet), else
	// "complete". Metrics are what's known so far either way.
	Status   string         `json:"status"`
	Currency string         `json:"currency"`
	PlanFrom string         `json:"plan_from"`
	Leg      PlanLeg        `json:"leg"`
	Metrics  []PlanMetric   `json:"metrics"`
	Stops    []PlanStopPair `json:"stops"`
}

// PlanLeg is the part of the outing compared: from leaving through the
// first arrival at the plan's destination.
type PlanLeg struct {
	StartedAt time.Time `json:"started_at"`
	// ArrivedAt is when the leg reached the destination; the time of
	// the latest drive's end while still on the way.
	ArrivedAt time.Time `json:"arrived_at"`
	Miles     float64   `json:"miles"`
	DriveIDs  []string  `json:"drive_ids"`
	ChargeIDs []string  `json:"charge_ids"`
}

// PlanMetric is one planned/actual pair. Delta is actual − planned;
// any of the three is null when unknown. The cost-only fields are
// omitted on other metrics.
type PlanMetric struct {
	Metric           string             `json:"metric"`
	Unit             string             `json:"unit"`
	Planned          *float64           `json:"planned"`
	Actual           *float64           `json:"actual"`
	Delta            *float64           `json:"delta"`
	ActualComplete   *bool              `json:"actual_complete,omitempty"`
	ActualKnown      *float64           `json:"actual_known,omitempty"`
	UnpricedSessions *int               `json:"unpriced_sessions,omitempty"`
	ActualSources    map[string]float64 `json:"actual_sources,omitempty"`
	Reasons          []PlanReason       `json:"reasons"`
}

// PlanReason explains part of a difference. Amount (cost metric only)
// is that part of the cost delta, in the comparison currency.
//
// Detail is ready-to-show copy (plan-vs-actual-copy.md); the structured
// fields, set where they apply, let a client word it itself. Stop and
// network names are public chargers only, never a home.
type PlanReason struct {
	Code         string   `json:"code"`
	Amount       *float64 `json:"amount"`
	Detail       string   `json:"detail"`
	Network      string   `json:"network,omitempty"`      // the actual session's network
	PlanNetwork  string   `json:"plan_network,omitempty"` // the planned stop's network
	Stop         string   `json:"stop,omitempty"`         // planned stop label
	Rate         *float64 `json:"rate,omitempty"`         // actual price per kWh
	PlanRate     *float64 `json:"plan_rate,omitempty"`
	Estimated    bool     `json:"estimated,omitempty"` // Rate is a network estimate, not a bill
	KWh          *float64 `json:"kwh,omitempty"`
	PlanKWh      *float64 `json:"plan_kwh,omitempty"`
	Count        *int     `json:"count,omitempty"`   // unpriced sessions, extra or skipped stops
	Percent      *float64 `json:"percent,omitempty"` // drove_further, less_efficient
	Miles        *float64 `json:"miles,omitempty"`
	PlanMiles    *float64 `json:"plan_miles,omitempty"`
	MiPerKWh     *float64 `json:"mi_per_kwh,omitempty"`
	PlanMiPerKWh *float64 `json:"plan_mi_per_kwh,omitempty"`
}

// money formats an amount as the copy does: whole units above 20,
// cents below.
func money(v float64, cur string) string {
	sym, suffix := currencySymbol(cur)
	if math.Abs(v) > 20 {
		return fmt.Sprintf("%s%.0f%s", sym, math.Abs(v), suffix)
	}
	return fmt.Sprintf("%s%.2f%s", sym, math.Abs(v), suffix)
}

// rate formats a price per kWh (always cents).
func rate(v float64, cur string) string {
	sym, suffix := currencySymbol(cur)
	return fmt.Sprintf("%s%.2f%s", sym, v, suffix)
}

func currencySymbol(cur string) (string, string) {
	switch strings.ToUpper(cur) {
	case "", "USD", "CAD", "AUD":
		return "$", ""
	case "EUR":
		return "€", ""
	case "GBP":
		return "£", ""
	}
	return "", " " + strings.ToUpper(cur)
}

// PlanStopPair pairs a planned stop with the actual session there.
// Match is same_station / same_network / other_network / nearby (paired,
// network unknown on one side) / skipped / extra.
type PlanStopPair struct {
	Match   string           `json:"match"`
	Planned *PlanStopPlanned `json:"planned"`
	Actual  *PlanStopActual  `json:"actual"`
}

// PlanStopPlanned is the planned side of a stop.
type PlanStopPlanned struct {
	Name       string   `json:"name"`
	Network    string   `json:"network"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	ArrivalSoC *float64 `json:"arrival_soc"`
	ChargeMin  *float64 `json:"charge_min"`
	EstKWh     *float64 `json:"est_kwh"`
	EstCost    *float64 `json:"est_cost"`
}

// PlanStopActual is the actual session at a stop.
type PlanStopActual struct {
	ChargeID   string    `json:"charge_id"`
	Kind       string    `json:"kind"` // dcfc | ac
	Network    string    `json:"network"`
	KWh        float64   `json:"kwh"`
	Cost       *float64  `json:"cost"` // null when not priced
	CostSource string    `json:"cost_source"`
	StartedAt  time.Time `json:"started_at"`
	Minutes    float64   `json:"minutes"`
}

// ComparePlan compares p with the outing tripID in `in` (§5). False
// when no outing with that id is in `in`.
func ComparePlan(p Plan, in Input, tripID string) (PlanComparison, bool) {
	var o []tripEvent
	for _, cand := range outingsOf(in) {
		if outingID(cand) == tripID {
			o = cand
			break
		}
	}
	if o == nil {
		return PlanComparison{}, false
	}
	leg, arrive, arrived := planLeg(p, o)
	cfg := in.Config
	status := "complete"
	if !arrived || outingLive(o, in) {
		status = "in_progress"
	}
	cmp := PlanComparison{Status: status, Currency: cfg.Currency, Metrics: []PlanMetric{}, Stops: []PlanStopPair{},
		Leg: PlanLeg{StartedAt: leg[0].start.UTC(), ArrivedAt: arrive.EndedAt.UTC(), DriveIDs: []string{}, ChargeIDs: []string{}}}
	if cmp.Currency == "" {
		cmp.Currency = p.Currency
	}

	var fast, slowAway []sessionView
	var legDrives []*Drive
	for _, e := range leg {
		switch {
		case e.drive != nil:
			legDrives = append(legDrives, e.drive)
			cmp.Leg.DriveIDs = append(cmp.Leg.DriveIDs, e.drive.ID)
		case e.charge != nil:
			cmp.Leg.ChargeIDs = append(cmp.Leg.ChargeIDs, e.charge.ID)
			v := viewOf(*e.charge, cfg)
			if v.kind == "dcfc" {
				fast = append(fast, v)
			} else if v.place != "home" && hasEnergy(*e.charge) {
				slowAway = append(slowAway, v)
			}
		}
	}

	cmp.Leg.Miles = drivenMiles(legDrives)
	pairs, costReasons := pairStops(p, fast, cfg.Currency)
	for _, v := range slowAway {
		pairs = append(pairs, PlanStopPair{Match: "extra", Actual: actualStop(v)})
	}
	cmp.Stops = pairs
	cmp.Metrics = append(cmp.Metrics,
		costMetric(p, cfg, fast, costReasons),
		energyMetric(p, legDrives),
		stopsMetric(p, pairs),
		socMetric(p, arrive, arrived),
		durationMetric(p, cmp.Leg, arrived))
	return cmp, true
}

// planLeg is the outing up to and including the first drive that ends
// near the destination, with the charges in between, and that arriving
// drive. When no drive gets there (still on the way, or the plan's
// destination was missed by a few km) the leg is the outing up to the
// drive that ends nearest it, and arrived is false only when none is
// within planFarKM (so the comparison is reported in progress).
func planLeg(p Plan, o []tripEvent) ([]tripEvent, *Drive, bool) {
	sp := planSpan(p, o)
	return o[sp.from : sp.to+1], o[sp.arrive].drive, sp.arrived
}

// legSpan is the part of an outing a plan covers: events from..to
// (inclusive), arrive the arriving drive's index.
type legSpan struct {
	from, to, arrive int
	arrived          bool
}

// planSpan finds the leg of outing o that carries out p. It ends at the
// first drive that ends near the destination (else the one that ends
// nearest it, within planFarKM); with neither (still on the way) it
// runs to the end of the outing, not arrived, and with no positions at
// all it's the whole outing. It starts at the last drive at or before
// that arrival that starts at the plan's origin (within planNearKM; else
// the last within planFarKM), so a plan that doesn't start at home (the
// drive back from a hotel) is compared with that leg, not the round
// trip; with neither, at the first drive.
// The outing must have a drive.
func planSpan(p Plan, o []tripEvent) legSpan {
	arriveAt, nearest, firstDrive, lastDrive := -1, -1, -1, -1
	nearestKM := math.Inf(1)
	for i, e := range o {
		if e.drive == nil {
			continue
		}
		if firstDrive < 0 {
			firstDrive = i
		}
		lastDrive = i
		if arriveAt >= 0 {
			continue
		}
		km, ok := kmTo(e.drive.EndLat, e.drive.EndLon, p.Destination)
		if !ok {
			continue
		}
		if km <= planNearKM {
			arriveAt = i
		} else if km < nearestKM {
			nearestKM, nearest = km, i
		}
	}
	sp := legSpan{from: firstDrive, to: len(o) - 1, arrive: lastDrive}
	switch {
	case arriveAt >= 0:
		sp.to, sp.arrive, sp.arrived = arriveAt, arriveAt, true
	case nearest >= 0 && nearestKM <= planFarKM: // ended a few km off the planned point
		sp.to, sp.arrive, sp.arrived = nearest, nearest, true
	case nearest < 0: // no positions recorded: take the outing as the trip
		sp.arrived = true
	}
	// Prefer the last drive that starts at the origin (≤ planNearKM), so
	// a coffee stop or pickup near home doesn't cut the leg short; fall
	// back to the last within planFarKM.
	for _, radius := range []float64{planNearKM, planFarKM} {
		found := false
		for i := sp.arrive; i >= 0; i-- {
			if d := o[i].drive; d != nil && within(d.StartLat, d.StartLon, p.Origin, radius) {
				sp.from, found = i, true
				break
			}
		}
		if found {
			break
		}
	}
	return sp
}

// pairStops pairs planned stops (route order) with the leg's fast
// sessions, and returns the cost reasons the pairing explains.
func pairStops(p Plan, fast []sessionView, currency string) ([]PlanStopPair, []PlanReason) {
	used := make([]bool, len(fast))
	var pairs []PlanStopPair
	var reasons []PlanReason
	for i := range p.Stops {
		s := p.Stops[i]
		at := LatLon{s.Lat, s.Lon}
		bestJ, bestKM := -1, math.Inf(1)
		for j, v := range fast {
			if used[j] {
				continue
			}
			if km, ok := kmTo(v.c.Lat, v.c.Lon, at); ok && km <= planStopNearKM && km < bestKM {
				bestJ, bestKM = j, km
			}
		}
		pl := plannedStop(s)
		if bestJ < 0 {
			pairs = append(pairs, PlanStopPair{Match: "skipped", Planned: pl})
			r := PlanReason{Code: "skipped_stop", Stop: stopLabel(s), PlanNetwork: s.Network}
			if s.EstCost != nil {
				r.Amount = ptrRound2(-*s.EstCost)
				r.Detail = fmt.Sprintf("You skipped a planned stop, saving about %s.", money(*s.EstCost, currency))
			} else {
				r.Detail = "You skipped a planned stop."
			}
			reasons = append(reasons, r)
			continue
		}
		used[bestJ] = true
		v := fast[bestJ]
		match := matchKind(s, v, bestKM)
		pairs = append(pairs, PlanStopPair{Match: match, Planned: pl, Actual: actualStop(v)})
		actualNet := strings.TrimSpace(v.c.Network)
		if match == "other_network" {
			reasons = append(reasons, PlanReason{Code: "other_network", Network: actualNet, PlanNetwork: s.Network,
				Stop: stopLabel(s), Detail: fmt.Sprintf("You used %s instead of the planned %s.", actualNet, s.Network)})
		}
		akwh := val(v.c.EnergyKWh)
		if !v.spendOK || akwh <= 0 || s.EstRate == nil {
			continue
		}
		ar, pr := *v.c.Cost/akwh, *s.EstRate
		net := actualNet
		if net == "" {
			net = stopLabel(s)
		}
		if pr > 0 && math.Abs(ar-pr)/pr > 0.05 {
			est := v.source == SourceNetworkEstimate
			r := PlanReason{Code: "rate_differed", Amount: ptrRound2((ar - pr) * akwh), Network: net, PlanNetwork: s.Network,
				Stop: stopLabel(s), Rate: ptrRound2(ar), PlanRate: ptrRound2(pr), Estimated: est}
			if est {
				r.Detail = fmt.Sprintf("%s's price is estimated at %s/kWh; the plan assumed %s.", net, rate(ar, currency), rate(pr, currency))
			} else {
				r.Detail = fmt.Sprintf("%s charged %s/kWh; the plan assumed %s.", net, rate(ar, currency), rate(pr, currency))
			}
			reasons = append(reasons, r)
		}
		if s.EstKWh != nil && *s.EstKWh > 0 && math.Abs(akwh-*s.EstKWh) / *s.EstKWh > 0.10 {
			more := "more"
			if akwh < *s.EstKWh {
				more = "less"
			}
			reasons = append(reasons, PlanReason{Code: "energy_differed", Amount: ptrRound2((akwh - *s.EstKWh) * pr),
				Network: net, Stop: stopLabel(s), KWh: ptrRound1(akwh), PlanKWh: ptrRound1(*s.EstKWh),
				Detail: fmt.Sprintf("You took %.0f kWh %s than planned at %s.", math.Abs(akwh-*s.EstKWh), more, stopLabel(s))})
		}
	}
	for j, v := range fast {
		if used[j] {
			continue
		}
		pairs = append(pairs, PlanStopPair{Match: "extra", Actual: actualStop(v)})
		r := PlanReason{Code: "extra_stop", Network: strings.TrimSpace(v.c.Network), Detail: "You made an extra fast-charge stop."}
		if v.spendOK {
			r.Amount = ptrRound2(*v.c.Cost)
			r.Detail = fmt.Sprintf("You made an extra fast-charge stop, which cost %s.", money(*v.c.Cost, currency))
		}
		reasons = append(reasons, r)
	}
	return pairs, reasons
}

func stopLabel(s PlannedStop) string {
	// Planned stops are public chargers from the planner, never a home.
	if s.Network != "" {
		return s.Network
	}
	if s.Name != "" {
		return s.Name
	}
	return "the planned charger"
}

// matchKind classifies a paired stop.
func matchKind(s PlannedStop, v sessionView, km float64) string {
	if km <= planSameStation {
		return "same_station"
	}
	ps, as := networkSlug(s.Network), networkSlug(v.c.Network)
	switch {
	case ps == "" || as == "":
		return "nearby"
	case ps == as:
		return "same_network"
	}
	return "other_network"
}

func networkSlug(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	if n := dcfcrates.MatchNetwork(name); n.Slug != dcfcrates.UnmatchedNetwork.Slug {
		return n.Slug
	}
	return ""
}

func plannedStop(s PlannedStop) *PlanStopPlanned {
	return &PlanStopPlanned{Name: s.Name, Network: s.Network, Lat: s.Lat, Lon: s.Lon,
		ArrivalSoC: roundPtr(s.ArrivalSoC, 0), ChargeMin: roundPtr(s.ChargeMin, 0),
		EstKWh: roundPtr(s.EstKWh, 1), EstCost: roundPtr(s.EstCost, 2)}
}

func actualStop(v sessionView) *PlanStopActual {
	a := &PlanStopActual{ChargeID: v.c.ID, Kind: v.kind, Network: strings.TrimSpace(v.c.Network),
		KWh: round1(val(v.c.EnergyKWh)), CostSource: v.source, StartedAt: v.c.StartedAt.UTC(),
		Minutes: math.Round(v.c.EndedAt.Sub(v.c.StartedAt).Minutes())}
	if v.spendOK {
		a.Cost = ptrRound2(*v.c.Cost)
	}
	return a
}

// costMetric is §5 cost under the §6 honesty rules.
func costMetric(p Plan, cfg Config, fast []sessionView, reasons []PlanReason) PlanMetric {
	m := PlanMetric{Metric: "cost", Unit: cfg.Currency, Reasons: []PlanReason{}}
	if m.Unit == "" {
		m.Unit = p.Currency
	}
	if p.Currency != "" && cfg.Currency != "" && !strings.EqualFold(p.Currency, cfg.Currency) {
		m.Reasons = append(m.Reasons, PlanReason{Code: "currency_mismatch",
			Detail: "The plan and your charges use different currencies."})
		return m
	}
	m.Planned = roundPtr(p.Cost, 2)
	known, unpriced := 0.0, 0
	sources := map[string]float64{SourceRivian: 0, SourceNetworkEstimate: 0, SourceHomeRate: 0, SourceUser: 0}
	for _, v := range fast {
		switch {
		case v.spendOK:
			known += *v.c.Cost
			sources[v.source] += *v.c.Cost
		case hasEnergy(v.c):
			unpriced++
		}
	}
	for k, x := range sources {
		sources[k] = math.Round(x*100) / 100
	}
	complete := unpriced == 0
	m.ActualComplete, m.UnpricedSessions, m.ActualSources = &complete, &unpriced, sources
	m.ActualKnown = ptrRound2(known)
	if complete {
		m.Actual = ptrRound2(known)
		m.Delta = diff(m.Actual, m.Planned)
	}
	m.Reasons = append(m.Reasons, reasons...)
	if est := sources[SourceNetworkEstimate]; est > 0 {
		m.Reasons = append(m.Reasons, PlanReason{Code: "estimated", Estimated: true,
			Detail: "Part of this total is estimated from the network's price, not billed."})
	}
	if unpriced > 0 {
		n := unpriced
		m.Reasons = append(m.Reasons, PlanReason{Code: "unpriced", Count: &n,
			Detail: fmt.Sprintf("%d charge(s) aren't priced yet, so the total is incomplete.", unpriced)})
	}
	sortReasons(m.Reasons)
	return m
}

func energyMetric(p Plan, drives []*Drive) PlanMetric {
	m := PlanMetric{Metric: "energy_kwh", Unit: "kWh", Planned: roundPtr(p.EnergyKWh, 1), Reasons: []PlanReason{}}
	kwh, mi, partial := 0.0, 0.0, false
	for _, d := range drives {
		mi += val(d.DistanceMi)
		if positive(d.EnergyKWh) {
			kwh += *d.EnergyKWh
		} else {
			partial = true
		}
	}
	if partial || len(drives) == 0 {
		m.Reasons = append(m.Reasons, PlanReason{Code: "energy_partial",
			Detail: "One drive wasn't fully recorded, so energy may be off."})
	} else {
		m.Actual = ptrRound1(kwh)
		m.Delta = diff(m.Actual, m.Planned)
	}
	if p.DistanceMi != nil && mi > *p.DistanceMi*1.05 {
		pct := math.Round((mi / *p.DistanceMi - 1) * 100)
		m.Reasons = append(m.Reasons, PlanReason{Code: "drove_further", Percent: &pct,
			Miles: ptrRound1(mi), PlanMiles: ptrRound1(*p.DistanceMi),
			Detail: fmt.Sprintf("You drove %.0f%% further than planned.", pct)})
	}
	if !partial && kwh > 0 && mi > 0 && p.DistanceMi != nil && p.EnergyKWh != nil {
		planned, actual := *p.DistanceMi / *p.EnergyKWh, mi/kwh
		if actual < planned*0.95 {
			// Energy per mile, relative: (1/actual)/(1/planned) − 1.
			pct := math.Round((planned/actual - 1) * 100)
			m.Reasons = append(m.Reasons, PlanReason{Code: "less_efficient", Percent: &pct,
				MiPerKWh: ptrRound2(actual), PlanMiPerKWh: ptrRound2(planned),
				Detail: fmt.Sprintf("The drive used %.0f%% more energy per mile than planned.", pct)})
		}
	}
	return m
}

func stopsMetric(p Plan, pairs []PlanStopPair) PlanMetric {
	planned := float64(len(p.Stops))
	actual, extra, skipped := 0, 0, 0
	for _, s := range pairs {
		if s.Actual != nil && s.Actual.Kind == "dcfc" {
			actual++
		}
		switch {
		case s.Match == "extra" && s.Actual != nil && s.Actual.Kind == "dcfc":
			extra++
		case s.Match == "skipped":
			skipped++
		}
	}
	a := float64(actual)
	m := PlanMetric{Metric: "dcfc_stops", Unit: "count", Planned: &planned, Actual: &a, Reasons: []PlanReason{}}
	m.Delta = diff(m.Actual, m.Planned)
	if extra > 0 {
		n := extra
		m.Reasons = append(m.Reasons, PlanReason{Code: "extra_stop", Count: &n,
			Detail: fmt.Sprintf("%d more stop(s) than planned.", extra)})
	}
	if skipped > 0 {
		n := skipped
		m.Reasons = append(m.Reasons, PlanReason{Code: "skipped_stop", Count: &n,
			Detail: fmt.Sprintf("%d planned stop(s) skipped.", skipped)})
	}
	return m
}

func socMetric(p Plan, arrive *Drive, arrived bool) PlanMetric {
	m := PlanMetric{Metric: "arrival_soc", Unit: "pct", Planned: roundPtr(p.ArrivalSoC, 0), Reasons: []PlanReason{}}
	if arrived && arrive != nil && arrive.EndSoC != nil {
		m.Actual = roundPtr(arrive.EndSoC, 0)
	}
	m.Delta = diff(m.Actual, m.Planned)
	return m
}

func durationMetric(p Plan, leg PlanLeg, arrived bool) PlanMetric {
	m := PlanMetric{Metric: "duration", Unit: "min", Planned: roundPtr(p.DurationMin, 0), Reasons: []PlanReason{}}
	if d := leg.ArrivedAt.Sub(leg.StartedAt); arrived && d > 0 {
		v := math.Round(d.Minutes())
		m.Actual = &v
	}
	m.Delta = diff(m.Actual, m.Planned)
	return m
}

// sortReasons orders reasons by |amount|, largest first; reasons
// without an amount keep their order after them.
func sortReasons(rs []PlanReason) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i].Amount, rs[j].Amount
		switch {
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return math.Abs(*a) > math.Abs(*b)
	})
}

func diff(actual, planned *float64) *float64 {
	if actual == nil || planned == nil {
		return nil
	}
	return ptrRound2(*actual - *planned)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func ptrRound1(v float64) *float64 { r := round1(v); return &r }
func ptrRound2(v float64) *float64 { r := round2(v); return &r }

func roundPtr(p *float64, dp int) *float64 {
	if p == nil {
		return nil
	}
	f := math.Pow(10, float64(dp))
	r := math.Round(*p*f) / f
	return &r
}
