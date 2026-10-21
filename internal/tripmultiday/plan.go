// Package tripmultiday orchestrates a multi-day trip plan over the trip
// planner (Rivian's planTripWithMultiStopV2, or the replay planners on
// installs with no Rivian access). The planner is single-day: it computes one
// route through up to ten waypoints with charging stops, but has no concept of
// "sleep here, depart tomorrow."
//
// The orchestrator chains one planner call per day, daisied on departure SoC
// and departure time. Each day is a start, optional vias, and an end; day k+1
// starts where day k ended. Between days an overnight model projects how much
// the truck charges while parked (see overnight).
package tripmultiday

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// Limits. A day is one planner call, so it takes at most the planner's
// waypoint count; the day count keeps one request inside its time budget.
const (
	MaxDays           = 10
	MaxDayWaypoints   = 10   // the planner's limit per call, origin and destination included
	DefaultACMaxKW    = 11.5 // onboard AC charger ceiling when the model is unknown
	DefaultL2KW       = 7.0  // a typical hotel or home L2 when none is given
	DefaultReservePct = 10.0 // a day's arrival reserve when none was asked for
	MaxPlanTargetPct  = 90.0 // a raised reserve never asks for more than this
	maxReplans        = 2
	DefaultCapPct     = 80.0 // overnight charge stops here unless asked otherwise
	defaultParkedHrs  = 10.0 // when neither a departure nor an "until" time is known
	acEfficiency      = 0.9  // wall-to-battery efficiency of AC charging
	taperAbovePct     = 90.0 // above this the gain per kWh is halved
)

// Waypoint is a place on the trip. Label is cosmetic: never sent to the
// planner, echoed back so the SPA can name the stop.
type Waypoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	EntityID  string  `json:"entity_id,omitempty"`
	Label     string  `json:"label,omitempty"`
}

// Overnight describes the stop at the end of a day.
type Overnight struct {
	// L2KW is the available AC power. nil means "not given" and takes
	// DefaultL2KW; an explicit 0 means no charging at this stop.
	L2KW *float64
	// Hookup is the connector an own stop was entered with ("nema_14_50", ...);
	// it is echoed in the result. L2KWGiven is whether the caller sent L2KW: when
	// it did not, the power is a default for the hookup and the result says so.
	Hookup    string
	L2KWGiven bool
	// Until is the local time ("15:04") the truck is wanted the next morning.
	// The next day departs at the next such time after arrival.
	Until string
	// ParkedHours, when positive, fixes the stay: the next day departs that
	// long after arrival (the older request shape).
	ParkedHours float64
	// MaxSoCPct caps the post-charge SoC; 0 takes the request's default.
	MaxSoCPct float64
	Name      string
}

// DayInput is one day of the trip.
type DayInput struct {
	Vias []Waypoint
	End  Waypoint
	// DepartAt overrides when this day leaves (day 1: the request's StartAt).
	DepartAt *time.Time
	// TargetArrivalSoCPct is this day's reserve; nil takes the request's.
	TargetArrivalSoCPct *float64
	// Overnight ends the day; ignored on the last day.
	Overnight *Overnight
}

// Request is the orchestrator's input.
type Request struct {
	// Account-level inputs (forwarded to every day's planner call).
	VehicleID      string
	HasAdapter     *bool
	DriveMode      string
	TrailerProfile string

	// PackKWh is the usable pack, in kWh. Required for the overnight model.
	PackKWh float64
	// StartingSoC is the SoC at the start of day 1.
	StartingSoC float64

	// StartAt is when day 1 leaves; the zero value means Now.
	StartAt time.Time
	// Loc is the zone "until" times are read in; nil is UTC.
	Loc *time.Location
	// Now is the clock for an unset StartAt; nil is time.Now.
	Now func() time.Time

	Origin Waypoint
	Days   []DayInput

	// MaxOvernightSoCPct is the default overnight cap (0: DefaultCapPct).
	MaxOvernightSoCPct float64
	// ACMaxKW is the truck's AC ceiling (0: DefaultACMaxKW).
	ACMaxKW float64
	// TargetArrivalSoCPct is every day's reserve unless the day sets one.
	TargetArrivalSoCPct *float64

	NetworkPreferences []rivian.NetworkPreference

	// AdjustArrival, when set, is asked for each planned day (day is 1-based,
	// startSoC and target as the day was planned) for the arrival SoC after the
	// caller's corrections (weather, vehicle profile). The overnight and the
	// next day then start from the lower of that and the plan's own arrival, so
	// the chain is as conservative as what the user is shown. ok=false leaves
	// the plan's arrival in use.
	AdjustArrival func(ctx context.Context, day int, plan *rivian.TripPlan, startSoC float64, target *float64) (arrival float64, ok bool)
	// RoutePreferences applies to every day.
	RoutePreferences *rivian.RoutePreferences
}

// Response is the aggregated multi-day plan.
type Response struct {
	Days  []Day  `json:"days"`
	Total Totals `json:"total"`
	// Simulated is true when any day's plan was not the gateway's.
	Simulated bool `json:"simulated,omitempty"`
	// FailedDay is the day that could not be planned (1-based); the days
	// before it are intact and chaining stopped there, because the days after
	// cannot know their starting SoC. Error says why.
	FailedDay int    `json:"failed_day,omitempty"`
	Error     string `json:"error,omitempty"`
	// FailedErr is Error as an error, for the caller to classify. Not sent.
	FailedErr error `json:"-"`
	// PlannerFailure is true when FailedErr came from the planner rather than
	// from the orchestrator's own checks; its text is for the logs, not for
	// the user (the caller words the message).
	PlannerFailure bool `json:"-"`
}

// Day is one day's plan plus the overnight that ends it.
type Day struct {
	Index        int              `json:"index"` // 1-based
	Plan         *rivian.TripPlan `json:"plan"`
	DepartAt     time.Time        `json:"depart_at"`
	ArriveAt     time.Time        `json:"arrive_at"`
	Start        Waypoint         `json:"start"`
	Vias         []Waypoint       `json:"vias,omitempty"`
	End          Waypoint         `json:"end"`
	DepartureSoC float64          `json:"departure_soc"` // SoC at day start
	ArrivalSoC   float64          `json:"arrival_soc"`   // SoC at day end (route 0)
	// ChainArrivalSoC is the arrival SoC the overnight and the next day were
	// worked from: the lower of the plan's and the corrected one. Set only
	// when a correction applied.
	ChainArrivalSoC    float64          `json:"chain_arrival_soc,omitempty"`
	DestinationReached bool             `json:"destination_reached"`
	SoCBelowLimit      bool             `json:"soc_below_limit"`
	Overnight          *OvernightResult `json:"overnight,omitempty"`
	// Problem is plain text for the user when the day cannot be driven as
	// planned: it does not reach its end, or ends below the reserve with no stop
	// that fixes it. Empty for a day that works.
	Problem string `json:"problem,omitempty"`
}

// OvernightResult is the post-charge state at the end of a day. Always nil on
// the final day.
type OvernightResult struct {
	Name        string  `json:"name,omitempty"`
	ParkedHours float64 `json:"parked_hours"`
	L2KW        float64 `json:"l2_kw"`
	// Hookup echoes an own stop's connector; L2KWAssumed is true when the power
	// is the default for it rather than a value the user gave.
	Hookup      string `json:"hookup,omitempty"`
	L2KWAssumed bool   `json:"l2_kw_assumed,omitempty"`
	// AddedKWh is the energy that went into the battery.
	AddedKWh         float64 `json:"added_kwh"`
	PostChargeSoCPct float64 `json:"post_charge_soc_pct"`
	// Capped reports the cap stopped the charge short of what the stay allowed.
	Capped bool `json:"capped"`
}

// Totals summarises the chained-day plan so the SPA can show one
// "Trip total: X mi, Y h drive, Z stops" line above the day cards.
type Totals struct {
	DistanceMeters      float64 `json:"distance_meters"`
	DriveDurationSec    int     `json:"drive_duration_sec"`
	ChargingDurationSec int     `json:"charging_duration_sec"`
}

// Planner is what the orchestrator needs; tests plug a fake.
type Planner = rivian.TripPlanner

// RequestError is a request the orchestrator refuses; Day is the 1-based day
// at fault, or 0 for the request as a whole. The handler answers it with 400.
type RequestError struct {
	Day int
	Msg string
}

func (e *RequestError) Error() string {
	if e.Day > 0 {
		return fmt.Sprintf("day %d: %s", e.Day, e.Msg)
	}
	return e.Msg
}

func reqErr(day int, format string, a ...any) error {
	return &RequestError{Day: day, Msg: fmt.Sprintf(format, a...)}
}

// Plan runs the orchestrator. It returns an error only for a request it
// refuses (a *RequestError). A day the planner fails on is not an error: the
// days so far come back with FailedDay and Error set.
func Plan(ctx context.Context, p Planner, req Request) (*Response, error) {
	if p == nil {
		return nil, errors.New("planner is nil")
	}
	if req.PackKWh <= 0 {
		return nil, reqErr(0, "pack_kwh required for multi-day planning")
	}
	if req.StartingSoC <= 0 || req.StartingSoC > 100 {
		return nil, reqErr(0, "starting_soc must be between 0 and 100")
	}
	if len(req.Days) == 0 {
		return nil, reqErr(0, "at least one day required")
	}
	if len(req.Days) > MaxDays {
		return nil, reqErr(0, "%d days (the limit is %d)", len(req.Days), MaxDays)
	}
	for i, d := range req.Days {
		if n := len(d.Vias) + 2; n > MaxDayWaypoints {
			return nil, reqErr(i+1, "%d waypoints (start, %d vias, end; the limit is %d)", n, len(d.Vias), MaxDayWaypoints)
		}
		if o := d.Overnight; o != nil && o.Until != "" {
			if _, _, err := parseClock(o.Until); err != nil {
				return nil, reqErr(i+1, "overnight until %q is not a HH:MM time", o.Until)
			}
		}
	}

	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	loc := req.Loc
	if loc == nil {
		loc = time.UTC
	}
	acMax := req.ACMaxKW
	if acMax <= 0 {
		acMax = DefaultACMaxKW
	}
	defCap := req.MaxOvernightSoCPct
	if defCap <= 0 {
		defCap = DefaultCapPct
	}

	depart := req.StartAt
	if depart.IsZero() {
		depart = now().Truncate(time.Minute)
	}
	soc := req.StartingSoC
	start := req.Origin
	resp := &Response{Days: make([]Day, 0, len(req.Days))}

	for i, di := range req.Days {
		if di.DepartAt != nil {
			depart = *di.DepartAt
		}
		target := req.TargetArrivalSoCPct
		if di.TargetArrivalSoCPct != nil {
			target = di.TargetArrivalSoCPct
		}
		in := rivian.PlanTripInput{
			VehicleID:               req.VehicleID,
			StartingSoC:             soc,
			HasAdapter:              req.HasAdapter,
			DriveMode:               req.DriveMode,
			TrailerProfile:          req.TrailerProfile,
			DepartureTime:           depart,
			TargetArrivalSocPercent: target,
			NetworkPreferences:      req.NetworkPreferences,
			RoutePreferences:        req.RoutePreferences,
			Waypoints:               dayWaypoints(start, di),
		}
		plan, err := p.PlanTrip(ctx, in)
		if err != nil {
			resp.FailedDay, resp.FailedErr, resp.PlannerFailure = i+1, err, true
			resp.Error = fmt.Sprintf("day %d could not be planned", i+1)
			return resp, nil
		}
		if plan == nil || len(plan.Routes) == 0 {
			resp.FailedDay, resp.FailedErr, resp.PlannerFailure = i+1, errors.New("the planner returned no route"), true
			resp.Error = fmt.Sprintf("day %d could not be planned", i+1)
			return resp, nil
		}

		// The corrected arrival (weather, vehicle profile) can fall below the
		// reserve the day was planned for. Plan the day again with the reserve
		// raised by the shortfall, so the planner adds the stop that makes up
		// for it, rather than handing the user a day that ends under its reserve.
		var adj float64
		var adjOK bool
		if req.AdjustArrival != nil && i < len(req.Days)-1 {
			adj, adjOK = req.AdjustArrival(ctx, i+1, plan, soc, target)
			want := DefaultReservePct
			if target != nil {
				want = *target
			}
			for try := 0; try < maxReplans && adjOK && plan.Routes[0].DestinationReached && adj < want; try++ {
				raised := math.Min(want+(want-adj)+1, MaxPlanTargetPct)
				if in.TargetArrivalSocPercent != nil && raised <= *in.TargetArrivalSocPercent {
					break
				}
				in2 := in
				in2.TargetArrivalSocPercent = &raised
				p2, err := p.PlanTrip(ctx, in2)
				if err != nil || p2 == nil || len(p2.Routes) == 0 || !p2.Routes[0].DestinationReached {
					break
				}
				a2, ok2 := req.AdjustArrival(ctx, i+1, p2, soc, target)
				if !ok2 {
					break
				}
				plan, adj, in = p2, a2, in2
			}
		}

		rt := plan.Routes[0]
		day := Day{
			Index: i + 1, Plan: plan, Start: start, Vias: di.Vias, End: di.End,
			DepartAt: depart.UTC(), ArriveAt: arrivalOf(rt, depart).UTC(),
			DepartureSoC: soc, ArrivalSoC: rt.ArrivalSoC,
			DestinationReached: rt.DestinationReached, SoCBelowLimit: plan.SoCBelowLimit,
		}
		if !day.DestinationReached {
			day.Problem = fmt.Sprintf("The truck can't reach the end of day %d from where it starts: it would run out of charge with no fast charger in range. Charge more overnight, start with a higher charge, or split the day.", i+1)
		} else if day.SoCBelowLimit || (adjOK && adj < wantReserve(target)) {
			day.Problem = fmt.Sprintf("Day %d ends below your reserve and no stop makes up for it. Charge more beforehand or add a stop.", i+1)
		}
		resp.Total.DistanceMeters += rt.TotalDriveDistanceMeters
		resp.Total.DriveDurationSec += rt.TotalDriveDurationSec
		resp.Total.ChargingDurationSec += rt.TotalChargingDurationSec
		if plan.Simulated {
			resp.Simulated = true
		}

		last := i == len(req.Days)-1
		if !last {
			next := req.Days[i+1]
			ov := di.Overnight
			if ov == nil {
				ov = &Overnight{}
			}
			chainSoC := day.ArrivalSoC
			if adjOK {
				if adj < chainSoC {
					chainSoC = adj
				}
				day.ChainArrivalSoC = chainSoC
			}
			nextDepart := nextDeparture(day.ArriveAt, ov, next.DepartAt, loc)
			if nextDepart.Before(day.ArriveAt) {
				resp.Days = append(resp.Days, day)
				resp.FailedDay = i + 2
				resp.FailedErr = fmt.Errorf("day %d departs before day %d arrives", i+2, i+1)
				resp.Error = fmt.Sprintf("day %d could not be planned: it departs at %s, before day %d arrives at %s",
					i+2, nextDepart.UTC().Format(time.RFC3339), i+1, day.ArriveAt.Format(time.RFC3339))
				return resp, nil
			}
			hours := nextDepart.Sub(day.ArriveAt).Hours()
			capPct := defCap
			if ov.MaxSoCPct > 0 {
				capPct = ov.MaxSoCPct
			}
			res := overnight(chainSoC, hours, ov, req.PackKWh, acMax, capPct)
			day.Overnight = res
			soc = res.PostChargeSoCPct
			start = di.End
			depart = nextDepart
		}
		resp.Days = append(resp.Days, day)

		if !last && !day.DestinationReached {
			// The next day would start from a place the truck never reaches.
			resp.FailedDay = i + 2
			resp.Error = fmt.Sprintf("day %d could not be planned: day %d does not reach its end", i+2, i+1)
			resp.FailedErr = fmt.Errorf("day %d does not reach its end", i+1)
			return resp, nil
		}
		if soc < 1 { // a planner needs some charge to start from
			soc = 1
		}
	}
	return resp, nil
}

func dayWaypoints(start Waypoint, d DayInput) []rivian.PlanTripWaypoint {
	wps := make([]rivian.PlanTripWaypoint, 0, len(d.Vias)+2)
	wps = append(wps, rivian.PlanTripWaypoint{Latitude: start.Latitude, Longitude: start.Longitude, WaypointType: "origin", EntityID: start.EntityID})
	for _, v := range d.Vias {
		wps = append(wps, rivian.PlanTripWaypoint{Latitude: v.Latitude, Longitude: v.Longitude, WaypointType: "waypoint", EntityID: v.EntityID})
	}
	wps = append(wps, rivian.PlanTripWaypoint{Latitude: d.End.Latitude, Longitude: d.End.Longitude, WaypointType: "destination", EntityID: d.End.EntityID})
	return wps
}

// arrivalOf is when the route reaches its end: the planner's own arrival
// time at the last waypoint, else the departure plus the route's duration.
func arrivalOf(rt rivian.TripRoute, depart time.Time) time.Time {
	if n := len(rt.Waypoints); n > 0 {
		if t, err := time.Parse(time.RFC3339, rt.Waypoints[n-1].ArrivalTimeUTC); err == nil {
			return t
		}
	}
	return depart.Add(time.Duration(rt.TotalTripDurationSec) * time.Second)
}

// nextDeparture is when the day after an overnight leaves: its own explicit
// departure; else a fixed stay; else the next local "until" time after
// arrival; else a default stay.
func nextDeparture(arrive time.Time, ov *Overnight, explicit *time.Time, loc *time.Location) time.Time {
	if explicit != nil {
		return *explicit
	}
	if ov.ParkedHours > 0 {
		return arrive.Add(time.Duration(ov.ParkedHours * float64(time.Hour)))
	}
	if ov.Until != "" {
		if h, m, err := parseClock(ov.Until); err == nil {
			a := arrive.In(loc)
			t := time.Date(a.Year(), a.Month(), a.Day(), h, m, 0, 0, loc)
			if !t.After(a) {
				t = time.Date(a.Year(), a.Month(), a.Day()+1, h, m, 0, 0, loc)
			}
			return t
		}
	}
	return arrive.Add(time.Duration(defaultParkedHrs * float64(time.Hour)))
}

func parseClock(s string) (h, m int, err error) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, 0, errors.New("no colon")
	}
	if h, err = strconv.Atoi(hs); err != nil {
		return 0, 0, err
	}
	if m, err = strconv.Atoi(ms); err != nil {
		return 0, 0, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, errors.New("out of range")
	}
	return h, m, nil
}

// overnight is the AC charge while parked. The power is the stop's L2 (7 kW
// when not given; an explicit 0 is none) limited to the truck's AC ceiling,
// at 90 % wall-to-battery efficiency. The gain is halved for the part above
// 90 % SoC, and the charge stops at the cap (it never lowers the SoC: a truck
// that arrives above the cap leaves as it came).
func overnight(arrSoC, hours float64, ov *Overnight, packKWh, acMaxKW, capPct float64) *OvernightResult {
	kw := DefaultL2KW
	if ov.L2KW != nil {
		kw = *ov.L2KW
	}
	res := &OvernightResult{Name: ov.Name, ParkedHours: round2(hours), L2KW: kw, PostChargeSoCPct: arrSoC,
		Hookup: ov.Hookup, L2KWAssumed: ov.Hookup != "" && !ov.L2KWGiven}
	if kw <= 0 || hours <= 0 {
		return res
	}
	offeredPct := math.Min(kw, acMaxKW) * hours * acEfficiency / packKWh * 100
	gain := tapered(arrSoC, offeredPct)
	post := arrSoC + gain
	limit := math.Max(capPct, arrSoC)
	if post > limit {
		post, res.Capped = limit, true
	}
	res.PostChargeSoCPct = round2(post)
	res.AddedKWh = round2((post - arrSoC) / 100 * packKWh)
	return res
}

// tapered turns an offered gain (SoC points at full rate) into the gain
// achieved from arr: full rate up to 90 %, half rate above.
func tapered(arr, offered float64) float64 {
	if arr >= taperAbovePct {
		return offered / 2
	}
	room := taperAbovePct - arr
	if offered <= room {
		return offered
	}
	return room + (offered-room)/2
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// OvernightSoC projects the state of charge after parking for hours at an L2
// of l2kw (nil: the default; 0: none), from arrSoC, with the same model the
// planner chains days with. The AC ceiling is the default one and the charge
// stops at capPct (0: DefaultCapPct).
func OvernightSoC(arrSoC, hours float64, l2kw *float64, packKWh, capPct float64) float64 {
	if packKWh <= 0 {
		return arrSoC
	}
	if capPct <= 0 {
		capPct = DefaultCapPct
	}
	return overnight(arrSoC, hours, &Overnight{L2KW: l2kw}, packKWh, DefaultACMaxKW, capPct).PostChargeSoCPct
}

func wantReserve(target *float64) float64 {
	if target != nil {
		return *target
	}
	return DefaultReservePct
}
