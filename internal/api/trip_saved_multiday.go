package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// A saved multi-day trip keeps the plan itself, not just its inputs:
// saved_trips.plan is {"kind":"multiday","days":[...]}, the days as the
// plan-multiday endpoint returned them (plan, depart_at, arrive_at, start,
// vias, end, costs, overnight, ...). Reopening it needs no planner call, and
// plan vs actual compares each day with what was recorded.

const maxSavedDays = 10 // tripmultiday.MaxDays

// planSubject is what one plan-vs-actual is about: the whole of a single-day
// saved trip (day 0), or one day (1..N) of a multi-day one, as the inputs and
// plan JSON ledger.ParsePlan reads.
type planSubject struct {
	day    int
	inputs json.RawMessage
	plan   json.RawMessage
}

// savedDayJSON is the part of a stored day the comparison needs.
type savedDayJSON struct {
	Index    int             `json:"index"`
	Plan     json.RawMessage `json:"plan"`
	DepartAt string          `json:"depart_at"`
	ArriveAt string          `json:"arrive_at"`
	Costs    json.RawMessage `json:"costs"`
	Start    *savedPoint     `json:"start"`
	End      *savedPoint     `json:"end"`
	Vias     []savedPoint    `json:"vias"`
	// Overnight is the stay that ends the day (nil on the last day).
	Overnight *struct {
		L2KW     *float64 `json:"l2_kw"`
		MaxSoCPc float64  `json:"max_soc_pct"`
	} `json:"overnight"`
}

type savedPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
}

func (p *savedPoint) coords() (lat, lon float64) {
	if p.Lat == 0 && p.Lon == 0 {
		return p.Latitude, p.Longitude
	}
	return p.Lat, p.Lon
}

// savedMultiday returns the days of a stored multi-day plan, and whether the
// plan is one at all. A plan that says it is multi-day but is malformed is
// still "multi-day" with no days (validateSavedPlan refuses to save one).
func savedMultiday(plan json.RawMessage) ([]savedDayJSON, bool) {
	if len(plan) == 0 || plan[0] != '{' {
		return nil, false
	}
	var head struct {
		Kind string         `json:"kind"`
		Days []savedDayJSON `json:"days"`
	}
	if err := json.Unmarshal(plan, &head); err != nil || head.Kind != "multiday" {
		return nil, false
	}
	return head.Days, true
}

// subjectForDay builds the inputs and plan the matcher reads for day n (1..N):
// the day's departure time and endpoints as inputs, and its plan with its cost
// estimate alongside, the shape a saved single-day plan has.
func subjectForDay(days []savedDayJSON, n int) (planSubject, bool) {
	if n < 1 || n > len(days) {
		return planSubject{}, false
	}
	d := days[n-1]
	in := map[string]any{}
	if d.Start != nil {
		lat, lon := d.Start.coords()
		in["origin"] = map[string]any{"lat": lat, "lon": lon}
	}
	if d.End != nil {
		lat, lon := d.End.coords()
		in["destination"] = map[string]any{"lat": lat, "lon": lon}
	}
	if d.DepartAt != "" {
		in["departure_at"] = d.DepartAt
	}
	inputs, err := json.Marshal(in)
	if err != nil {
		return planSubject{}, false
	}
	plan := d.Plan
	if len(d.Costs) > 0 && string(d.Costs) != "null" {
		var m map[string]json.RawMessage
		if json.Unmarshal(d.Plan, &m) == nil {
			m["costs"] = d.Costs
			if merged, err := json.Marshal(m); err == nil {
				plan = merged
			}
		}
	}
	return planSubject{day: n, inputs: inputs, plan: plan}, true
}

// validateSavedPlan checks a plan about to be saved. Anything that is not a
// multi-day snapshot is left as it was; a multi-day one must have between 1 and
// 10 days, each with a plan object and (when it has them) usable endpoints.
func validateSavedPlan(plan json.RawMessage) error {
	if len(plan) == 0 || plan[0] != '{' {
		return nil
	}
	var head struct {
		Kind string            `json:"kind"`
		Days []json.RawMessage `json:"days"`
	}
	if err := json.Unmarshal(plan, &head); err != nil || head.Kind != "multiday" {
		return nil
	}
	if len(head.Days) == 0 {
		return errors.New("a multi-day plan needs at least one day")
	}
	if len(head.Days) > maxSavedDays {
		return fmt.Errorf("a multi-day plan has at most %d days", maxSavedDays)
	}
	for i, raw := range head.Days {
		var d savedDayJSON
		if err := json.Unmarshal(raw, &d); err != nil {
			return errors.New("day " + strconv.Itoa(i+1) + " is not valid")
		}
		if len(d.Plan) == 0 || d.Plan[0] != '{' {
			return errors.New("day " + strconv.Itoa(i+1) + " has no plan")
		}
		for _, p := range []*savedPoint{d.Start, d.End} {
			if p == nil {
				continue
			}
			lat, lon := p.coords()
			if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
				return errors.New("day " + strconv.Itoa(i+1) + " has an invalid coordinate")
			}
		}
	}
	return nil
}
