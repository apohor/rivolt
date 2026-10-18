package demo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripmultiday"
)

// The sample owner's Las Vegas trip as one saved multi-day trip: day 1 out
// (the "Las Vegas weekend" leg), a hotel night with a Level 2 charger, day 2
// back (the "Las Vegas back home" leg). Its days are those two saved plans, so
// Plan vs Actual pairs each day with the leg the replay drove, day by day.

// MultidayTripName is the saved multi-day trip's name; MultidayTripID its fixed
// id (also the demo preset's id).
const MultidayTripName = "Las Vegas 2-day"

// SampleMultidayTripID is the saved trip id of the two-day Las Vegas trip.
func SampleMultidayTripID() uuid.UUID { return SampleTripID(MultidayTripName) }

const (
	vegasOutName   = "Las Vegas weekend"
	vegasBackName  = "Las Vegas back home"
	hotelName      = "Sample hotel, Las Vegas"
	hotelL2KW      = 7.0
	hotelCapPct    = 80.0
	hotelDepartLoc = "08:00"
)

// multidayDay is a day as plan-multiday returns it: the orchestrator's day
// and its cost estimate.
type multidayDay struct {
	tripmultiday.Day
	Costs []tripadvice.CostEstimate `json:"costs,omitempty"`
}

// multidayBuild is the trip in the three forms it is stored in.
type multidayBuild struct {
	Inputs   json.RawMessage // the planner form (SavedTripInputs)
	Saved    json.RawMessage // the saved plan: {kind:"multiday", days, total}
	Response json.RawMessage // POST /api/trips/plan-multiday's answer
	Summary  PresetSummary
	DepartAt time.Time
	SavedAt  time.Time
}

// buildMultiday assembles the two-day trip from the sample's two leg plans,
// moving every time by shift (0 for the saved trip, which lines up with the
// replay; the preset uses presetDepart).
func buildMultiday(plans []rivian.SampleTripPlan, shift time.Duration, cfg settings.ChargingConfig, nets []settings.ChargingNetwork) (multidayBuild, error) {
	var out, back *rivian.SampleTripPlan
	for i := range plans {
		switch plans[i].Name {
		case vegasOutName:
			out = &plans[i]
		case vegasBackName:
			back = &plans[i]
		}
	}
	if out == nil || back == nil {
		return multidayBuild{}, errors.New("demo: the Las Vegas leg plans are missing")
	}
	legs := []rivian.SampleTripPlan{*out, *back}
	days := make([]multidayDay, 0, 2)
	var total tripmultiday.Totals
	for i, p := range legs {
		plan := shiftPlan(p.Plan, shift)
		if len(plan.Routes) == 0 || len(plan.Routes[0].Waypoints) < 2 {
			return multidayBuild{}, fmt.Errorf("demo: leg %q has no route", p.Name)
		}
		rt := plan.Routes[0]
		first, last := rt.Waypoints[0], rt.Waypoints[len(rt.Waypoints)-1]
		depart, err := time.Parse(time.RFC3339, first.DepartureTimeUTC)
		if err != nil {
			return multidayBuild{}, fmt.Errorf("demo: leg %q departure: %w", p.Name, err)
		}
		arrive, err := time.Parse(time.RFC3339, last.ArrivalTimeUTC)
		if err != nil {
			return multidayBuild{}, fmt.Errorf("demo: leg %q arrival: %w", p.Name, err)
		}
		tc := tripadvice.Context{PackKWh: p.PackKWh, HomePricePerKWh: cfg.HomePricePerKWh, HomeCurrency: cfg.HomeCurrency,
			GasPricePerGallon: cfg.GasPricePerGallon, ComparisonMPG: cfg.ComparisonMPG, DCFCNetworks: settings.AsOverrides(nets)}
		d := multidayDay{
			Day: tripmultiday.Day{
				Index: i + 1, Plan: &plan,
				DepartAt: depart.UTC(), ArriveAt: arrive.UTC(),
				Start:              tripmultiday.Waypoint{Latitude: p.Origin.Lat, Longitude: p.Origin.Lon, Label: p.Origin.Label},
				End:                tripmultiday.Waypoint{Latitude: p.Destination.Lat, Longitude: p.Destination.Lon, Label: p.Destination.Label},
				DepartureSoC:       p.StartSoC,
				ArrivalSoC:         rt.ArrivalSoC,
				DestinationReached: rt.DestinationReached,
			},
			Costs: []tripadvice.CostEstimate{tripadvice.EstimateRoute(&plan.Routes[0], tc)},
		}
		total.DistanceMeters += rt.TotalDriveDistanceMeters
		total.DriveDurationSec += rt.TotalDriveDurationSec
		total.ChargingDurationSec += rt.TotalChargingDurationSec
		days = append(days, d)
	}
	// The hotel night: day 1 ends in Las Vegas, day 2 leaves with what the leg
	// plan starts from; the charge is what takes it there.
	hours := days[1].DepartAt.Sub(days[0].ArriveAt).Hours()
	if hours <= 0 {
		return multidayBuild{}, errors.New("demo: the return leaves before the outbound arrives")
	}
	pack := legs[0].PackKWh
	arr, post := days[0].ArrivalSoC, days[1].DepartureSoC
	l2 := hotelL2KW
	if reach := tripmultiday.OvernightSoC(arr, hours, &l2, pack, 100); post > reach {
		post = reach // the hotel charger could not do more in that time
		days[1].DepartureSoC = post
	}
	days[0].Overnight = &tripmultiday.OvernightResult{
		Name: hotelName, ParkedHours: round2(hours), L2KW: hotelL2KW,
		AddedKWh: round2((post - arr) / 100 * pack), PostChargeSoCPct: round2(post),
		Capped: post >= hotelCapPct,
	}
	resp := struct {
		Days  []multidayDay       `json:"days"`
		Total tripmultiday.Totals `json:"total"`
		TZ    string              `json:"tz"`
	}{days, total, planLocation}
	rb, err := json.Marshal(resp)
	if err != nil {
		return multidayBuild{}, err
	}
	sb, err := json.Marshal(struct {
		Kind  string              `json:"kind"`
		Days  []multidayDay       `json:"days"`
		Total tripmultiday.Totals `json:"total"`
	}{"multiday", days, total})
	if err != nil {
		return multidayBuild{}, err
	}
	ib, err := multidayInputs(legs[0], legs[1], days[1].DepartAt)
	if err != nil {
		return multidayBuild{}, err
	}
	stops := 0
	for _, d := range days {
		for _, w := range d.Plan.Routes[0].Waypoints {
			if w.WaypointType == "DC_CHARGE_STATION" {
				stops++
			}
		}
	}
	end := days[1].Plan.Routes[0]
	return multidayBuild{
		Inputs: ib, Saved: sb, Response: rb,
		Summary: PresetSummary{
			DistanceMi:    math.Round(total.DistanceMeters/1609.344*10) / 10,
			DurationMin:   math.Round(float64(total.DriveDurationSec+total.ChargingDurationSec) / 60),
			ChargeStops:   stops,
			ArrivalSoCPct: end.ArrivalSoC,
		},
		DepartAt: days[0].DepartAt,
		SavedAt:  legs[0].SavedAt,
	}, nil
}

// multidayInputs is the planner form for the trip: home out to Las Vegas, a
// hotel night, and back to the same place.
func multidayInputs(out, back rivian.SampleTripPlan, backDepart time.Time) (json.RawMessage, error) {
	loc, err := time.LoadLocation(planLocation)
	if err != nil {
		return nil, fmt.Errorf("demo: plan location: %w", err)
	}
	pt := func(p rivian.SamplePlace) map[string]any {
		return map[string]any{"lat": p.Lat, "lon": p.Lon, "label": p.Label}
	}
	in := map[string]any{
		"origin":       pt(out.Origin),
		"destination":  pt(back.Destination),
		"extra_stops":  []any{pt(out.Destination)},
		"target_soc":   strconv.FormatFloat(out.TargetSoC, 'f', -1, 64),
		"starting_soc": strconv.FormatFloat(out.StartSoC, 'f', -1, 64),
		"drive_mode":   "EVERYDAY",
		"has_adapter":  true,
		"departure_at": out.DepartAt.In(loc).Format("2006-01-02T15:04"),
		// One overnight, at the Las Vegas stop.
		"overnight_flags":           []bool{true},
		"max_overnight_soc_pct":     hotelCapPct,
		"overnight_l2_kw":           hotelL2KW,
		"overnight_departure_local": hotelDepartLoc,
		"overnight_settings": []map[string]any{{
			"lat": out.Destination.Lat, "lon": out.Destination.Lon,
			"until": backDepart.In(loc).Format("15:04"), "l2_kw": hotelL2KW, "max_soc_pct": hotelCapPct,
		}},
	}
	return json.Marshal(in)
}

// SampleMultidayPlan is the saved two-day Las Vegas trip for the latest sample
// road trip over by now, with its times where the replay drove it, or nil when
// there is no such trip.
func SampleMultidayPlan(now, since time.Time, cfg settings.ChargingConfig, nets []settings.ChargingNetwork) (*SamplePlan, error) {
	plans := rivian.SampleTripPlans(now, since)
	if len(plans) < 2 {
		return nil, nil
	}
	b, err := buildMultiday(plans, 0, cfg, nets)
	if err != nil {
		return nil, err
	}
	return &SamplePlan{ID: SampleMultidayTripID(), Name: MultidayTripName, SavedAt: b.SavedAt.Add(time.Second), Inputs: b.Inputs, Plan: b.Saved, DepartAt: b.DepartAt}, nil
}

// vegasMultidayPreset is the same trip as a demo preset: a saved multi-day plan
// ({kind:"multiday", days, total}, what the picker opens like a saved trip) with
// the times moved to presetDepart, under the saved trip's id.
func vegasMultidayPreset() (presetFile, error) {
	depart, err := presetDepart()
	if err != nil {
		return presetFile{}, err
	}
	plans := rivian.SampleTripPlans(presetPlansAt, time.Time{})
	if len(plans) < 2 {
		return presetFile{}, errors.New("demo: the Las Vegas leg plans are missing")
	}
	var out *rivian.SampleTripPlan
	for i := range plans {
		if plans[i].Name == vegasOutName {
			out = &plans[i]
		}
	}
	if out == nil {
		return presetFile{}, errors.New("demo: the Las Vegas outbound plan is missing")
	}
	b, err := buildMultiday(plans, depart.Sub(out.DepartAt), presetConfig(), settings.DefaultChargingNetworks())
	if err != nil {
		return presetFile{}, err
	}
	return presetFile{Preset: Preset{ID: SampleMultidayTripID().String(), Name: MultidayTripName, Inputs: b.Inputs, Summary: b.Summary}, Plan: b.Saved}, nil
}
