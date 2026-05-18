// Package tripmultiday orchestrates a multi-day trip plan over
// Rivian's planTripWithMultiStopV2. Rivian's planner is single-day:
// it computes one origin → destination route with charging stops, but
// has no concept of "sleep here, depart tomorrow."
//
// The orchestrator chains N+1 single-day plans (N overnight stops),
// daisied on departure SoC. Between days, an overnight L2 model
// projects how much the vehicle will charge while parked:
//
//	post_charge_pct = arrival_pct + (L2_kW × parked_hours / pack_kWh) × 100
//	departure_pct   = min(post_charge_pct, max_overnight_soc_pct)
//
// When the overnight has no L2 (rural friend's house), departure SoC
// equals arrival SoC. The user picks the daily route knowing the
// constraint.
package tripmultiday

import (
	"context"
	"fmt"

	"github.com/apohor/rivolt/internal/rivian"
)

// Request is the orchestrator's input.
type Request struct {
	// Account-level inputs (forwarded to every leg's PlanTrip call).
	VehicleID  string
	HasAdapter *bool
	DriveMode  string

	// PackKWh is the vehicle's usable pack capacity, in kWh. Required
	// to compute overnight L2 SoC gain.
	PackKWh float64

	// StartingSoC is the SoC at the *start of day 1*.
	StartingSoC float64

	// Origin is day 1's start point.
	Origin Waypoint

	// Overnights are sleep stops, in order. Each marks the end of one
	// day and the start of the next.
	Overnights []OvernightStop

	// Destination is the trip's final endpoint (end of day N+1).
	Destination Waypoint

	// MaxOvernightSoCPct caps the post-overnight SoC. 0 defaults to 80%
	// the moment any overnight has an L2 hookup; ignored when no
	// overnight charges.
	MaxOvernightSoCPct float64

	// TargetArrivalSoCPct is forwarded to every leg. Same semantics as
	// the single-day plan-trip handler.
	TargetArrivalSoCPct *float64

	NetworkPreferences []rivian.NetworkPreference
}

// Waypoint is the origin / destination / overnight location.
type Waypoint struct {
	Latitude  float64
	Longitude float64
	EntityID  string
}

// OvernightStop describes a sleep stop between two days.
type OvernightStop struct {
	Waypoint
	// Name is purely cosmetic ("Holiday Inn Express"); never sent to
	// Rivian, surfaced back in the response so the SPA can label the
	// day boundary.
	Name string
	// ParkedHours is how long the vehicle is plugged in overnight.
	// 0 falls back to a conservative 10 hours.
	ParkedHours float64
	// L2KW is the available AC charging power in kW. 0 means no
	// charging at this stop — arrival SoC carries through to the next
	// day's departure unchanged.
	L2KW float64
}

// Response is the aggregated multi-day plan.
type Response struct {
	Days  []Day  `json:"days"`
	Total Totals `json:"total"`
}

// Day is one day's plan plus the overnight that ends it.
type Day struct {
	Index        int               `json:"index"`         // 1-based.
	Plan         *rivian.TripPlan  `json:"plan"`          // single-day plan from Rivian.
	DepartureSoC float64           `json:"departure_soc"` // SoC at day start.
	ArrivalSoC   float64           `json:"arrival_soc"`   // SoC at day end (route 0).
	Overnight    *OvernightResult  `json:"overnight,omitempty"`
}

// OvernightResult is the post-charge state at the end of a day.
// Always nil on the final day (destination day has no overnight).
type OvernightResult struct {
	Name             string  `json:"name,omitempty"`
	ParkedHours      float64 `json:"parked_hours"`
	L2KW             float64 `json:"l2_kw"`
	AddedKWh         float64 `json:"added_kwh"`
	PostChargeSoCPct float64 `json:"post_charge_soc_pct"`
	// Capped reports that the L2 had more energy on offer than the
	// max-overnight cap allowed; departure SoC sits at the cap.
	Capped bool `json:"capped"`
}

// Totals summarises the chained-day plan so the SPA can show one
// "Trip total: X mi, Y h drive, Z stops" line above the day cards.
type Totals struct {
	DistanceMeters     float64 `json:"distance_meters"`
	DriveDurationSec   int     `json:"drive_duration_sec"`
	ChargingDurationSec int    `json:"charging_duration_sec"`
}

// Planner is the rivian.Client subset we need. Defined here so tests
// can plug a fake without dragging the whole client.
type Planner interface {
	PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error)
}

// Plan runs the orchestrator. Returns an error only on hard failures
// (any leg PlanTrip call errors, or required input is missing).
// Per-leg DestinationReached=false is surfaced through the response,
// not as a Go error — the SPA decides whether to abort or render the
// best-effort plan.
func Plan(ctx context.Context, p Planner, req Request) (*Response, error) {
	if p == nil {
		return nil, fmt.Errorf("planner is nil")
	}
	if req.PackKWh <= 0 {
		return nil, fmt.Errorf("pack_kwh required for multi-day planning")
	}
	if req.StartingSoC <= 0 {
		return nil, fmt.Errorf("starting_soc required")
	}

	// Build the leg sequence: each leg goes from "current point" to
	// "next overnight or destination".
	type leg struct {
		from, to Waypoint
		// Pointer back to the overnight that *ends* this leg, nil on
		// the destination leg.
		overnight *OvernightStop
	}
	legs := make([]leg, 0, len(req.Overnights)+1)
	prev := req.Origin
	for i := range req.Overnights {
		ov := &req.Overnights[i]
		legs = append(legs, leg{from: prev, to: ov.Waypoint, overnight: ov})
		prev = ov.Waypoint
	}
	legs = append(legs, leg{from: prev, to: req.Destination, overnight: nil})

	cap := req.MaxOvernightSoCPct
	if cap <= 0 {
		cap = 80
	}

	soc := req.StartingSoC
	resp := &Response{Days: make([]Day, 0, len(legs))}
	for i, lg := range legs {
		in := rivian.PlanTripInput{
			VehicleID:   req.VehicleID,
			StartingSoC: soc,
			HasAdapter:  req.HasAdapter,
			DriveMode:   req.DriveMode,
			Waypoints: []rivian.PlanTripWaypoint{
				{Latitude: lg.from.Latitude, Longitude: lg.from.Longitude, WaypointType: "origin", EntityID: lg.from.EntityID},
				{Latitude: lg.to.Latitude, Longitude: lg.to.Longitude, WaypointType: "destination", EntityID: lg.to.EntityID},
			},
			TargetArrivalSocPercent: req.TargetArrivalSoCPct,
			NetworkPreferences:      req.NetworkPreferences,
		}
		plan, err := p.PlanTrip(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("day %d plan: %w", i+1, err)
		}

		day := Day{Index: i + 1, Plan: plan, DepartureSoC: soc}
		if len(plan.Routes) > 0 {
			day.ArrivalSoC = plan.Routes[0].ArrivalSoC
			resp.Total.DistanceMeters += plan.Routes[0].TotalDriveDistanceMeters
			resp.Total.DriveDurationSec += plan.Routes[0].TotalDriveDurationSec
			resp.Total.ChargingDurationSec += plan.Routes[0].TotalChargingDurationSec
		}

		// Compute next-day departure SoC if this isn't the destination.
		if lg.overnight != nil {
			ov := lg.overnight
			hours := ov.ParkedHours
			if hours <= 0 {
				hours = 10
			}
			res := &OvernightResult{
				Name:        ov.Name,
				ParkedHours: hours,
				L2KW:        ov.L2KW,
			}
			next := day.ArrivalSoC
			if ov.L2KW > 0 {
				addedKWh := ov.L2KW * hours
				addedPct := addedKWh / req.PackKWh * 100
				next = day.ArrivalSoC + addedPct
				if next > cap {
					next = cap
					res.Capped = true
				}
				res.AddedKWh = addedKWh
			}
			res.PostChargeSoCPct = next
			day.Overnight = res
			soc = next
		}
		resp.Days = append(resp.Days, day)
	}
	return resp, nil
}
