package demo

import (
	"context"
	"database/sql"
	"encoding/json"
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

// The demo's saved trip plans (demo parity): the sample owner's plans
// for the Las Vegas trip (out and back, and both legs as one two-day trip
// with a hotel overnight), so Plan vs Actual pairs a saved plan (or each
// day of it) with the outing the replay drove. The corpus loops, so the
// plans move to each loop's trip once it is over: a pass rewrites them
// (the same rows) when the plan for now differs from the stored
// one.

// planLocation is where the sample owner plans their trips: the
// planner's departure field is local time without a zone.
const planLocation = "America/Los_Angeles"

// SamplePlan is one saved trip as the planner stores it.
type SamplePlan struct {
	ID       uuid.UUID // fixed: SampleTripID(Name)
	Name     string
	SavedAt  time.Time
	Inputs   json.RawMessage // the planner's SavedTripInputs
	Plan     json.RawMessage // rivian.TripPlan plus costs, as /api/trips/plan returns it
	DepartAt time.Time
}

// SampleTripID is the saved trip id of the sample plan called name:
// fixed, so the demo's preset trip for it (internal/demo/trips) carries
// the same id on every install.
func SampleTripID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://rivolt.dev/demo/saved-trips/"+name))
}

// SampleTripIDs maps each sample plan's name to its saved trip id.
func SampleTripIDs() map[string]uuid.UUID {
	out := map[string]uuid.UUID{}
	for _, name := range rivian.SampleTripPlanNames() {
		out[name] = SampleTripID(name)
	}
	out[rivian.SamplePlanVegasTwoDay] = SampleTripID(rivian.SamplePlanVegasTwoDay)
	return out
}

// SamplePlans returns the demo's saved plans for the latest sample road
// trip over by now that started at or after since (the demo truck's
// first recorded frame, so the trip is recorded whole), priced with the
// demo user's charging settings (cfg, nets) as /api/trips/plan prices a
// plan. Nil when there is no such trip.
func SamplePlans(now, since time.Time, cfg settings.ChargingConfig, nets []settings.ChargingNetwork) ([]SamplePlan, error) {
	var out []SamplePlan
	for _, p := range rivian.SampleTripPlans(now, since) {
		ib, err := plannerInputs(p.Origin, p.Destination, p.StartSoC, p.TargetSoC, p.DepartAt)
		if err != nil {
			return nil, err
		}
		pb, err := plannerResponse(p.Plan, p.PackKWh, cfg, nets)
		if err != nil {
			return nil, err
		}
		out = append(out, SamplePlan{ID: SampleTripID(p.Name), Name: p.Name, SavedAt: p.SavedAt, Inputs: ib, Plan: pb, DepartAt: p.DepartAt})
	}
	if days := rivian.SampleTwoDayPlan(now, since); days != nil {
		ib, err := twoDayInputs(days)
		if err != nil {
			return nil, err
		}
		pb, err := twoDaySnapshot(days, cfg, nets, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, SamplePlan{ID: SampleTripID(rivian.SamplePlanVegasTwoDay), Name: rivian.SamplePlanVegasTwoDay,
			SavedAt: days[0].SavedAt, Inputs: ib, Plan: pb, DepartAt: days[0].DepartAt})
	}
	return out, nil
}

// twoDayInputs is the planner form for the two-day trip: home to home with
// one overnight stop at the hotel, its L2 and charge cap, leaving when day 2
// does.
func twoDayInputs(days []rivian.SampleTripPlan) (json.RawMessage, error) {
	loc, err := time.LoadLocation(planLocation)
	if err != nil {
		return nil, fmt.Errorf("demo: plan location: %w", err)
	}
	home, hotel := days[0].Origin, days[0].Destination
	return json.Marshal(map[string]any{
		"origin":                map[string]any{"lat": home.Lat, "lon": home.Lon, "label": home.Label},
		"destination":           map[string]any{"lat": home.Lat, "lon": home.Lon, "label": home.Label},
		"extra_stops":           []any{map[string]any{"lat": hotel.Lat, "lon": hotel.Lon, "label": rivian.SampleHotelLabel}},
		"overnight_flags":       []bool{true},
		"max_overnight_soc_pct": rivian.SampleOvernightMaxSoC,
		"overnight_l2_kw":       rivian.SampleHotelKW,
		"overnight_settings": []any{map[string]any{"lat": hotel.Lat, "lon": hotel.Lon,
			"until": days[1].DepartAt.In(loc).Format("15:04"), "l2_kw": rivian.SampleHotelKW, "max_soc_pct": rivian.SampleOvernightMaxSoC}},
		"target_soc":   strconv.FormatFloat(days[0].TargetSoC, 'f', -1, 64),
		"starting_soc": strconv.FormatFloat(days[0].StartSoC, 'f', -1, 64),
		"drive_mode":   "EVERYDAY",
		"has_adapter":  true,
		"departure_at": days[0].DepartAt.In(loc).Format("2006-01-02T15:04"),
	})
}

// twoDayDay is one day of a saved multi-day snapshot, as the plan-multiday
// endpoint answers it.
type twoDayDay struct {
	tripmultiday.Day
	Costs []tripadvice.CostEstimate `json:"costs,omitempty"`
}

// twoDaySnapshot is the saved trip's plan as the planner saves a multi-day
// trip ({"kind":"multiday","request","days","total"}), each day priced as
// /api/trips/plan-multiday prices it. shapes, when given, are each day's
// road shape (polyline6), carried on the day's route for the map.
func twoDaySnapshot(days []rivian.SampleTripPlan, cfg settings.ChargingConfig, nets []settings.ChargingNetwork, shapes []string) (json.RawMessage, error) {
	loc, err := time.LoadLocation(planLocation)
	if err != nil {
		return nil, fmt.Errorf("demo: plan location: %w", err)
	}
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	point := func(p rivian.SamplePlace, label string) tripmultiday.Waypoint {
		return tripmultiday.Waypoint{Latitude: p.Lat, Longitude: p.Lon, Label: label}
	}
	tc := tripadvice.Context{PackKWh: days[0].PackKWh, HomePricePerKWh: cfg.HomePricePerKWh, HomeCurrency: cfg.HomeCurrency,
		GasPricePerGallon: cfg.GasPricePerGallon, ComparisonMPG: cfg.ComparisonMPG, DCFCNetworks: settings.AsOverrides(nets)}
	var total tripmultiday.Totals
	out := make([]json.RawMessage, len(days))
	for i := range days {
		d := days[i]
		r := d.Plan.Routes[0]
		arrive, err := time.Parse(time.RFC3339, r.Waypoints[len(r.Waypoints)-1].ArrivalTimeUTC)
		if err != nil {
			return nil, fmt.Errorf("demo: two-day plan day %d arrival: %w", i+1, err)
		}
		start, end := point(d.Origin, d.Origin.Label), point(d.Destination, d.Destination.Label)
		if i == 0 {
			end.Label = rivian.SampleHotelLabel
		} else {
			start.Label = rivian.SampleHotelLabel
		}
		day := twoDayDay{Day: tripmultiday.Day{Index: i + 1, Plan: &d.Plan, DepartAt: d.DepartAt.UTC(), ArriveAt: arrive.UTC(),
			Start: start, End: end, DepartureSoC: d.StartSoC, ArrivalSoC: r.ArrivalSoC,
			DestinationReached: r.DestinationReached, SoCBelowLimit: r.ArrivalSoC < d.TargetSoC},
			Costs: []tripadvice.CostEstimate{tripadvice.EstimateRoute(&r, tc)}}
		if i < len(days)-1 {
			// The hotel's L2 over the stay, up to the cap: the stay allows
			// far more, so the cap is what stops it.
			next := days[i+1]
			day.Overnight = &tripmultiday.OvernightResult{Name: rivian.SampleHotelLabel,
				ParkedHours: math.Round(next.DepartAt.Sub(arrive).Hours()*10) / 10, L2KW: rivian.SampleHotelKW,
				AddedKWh:         math.Round((next.StartSoC-r.ArrivalSoC)/100*d.PackKWh*10) / 10,
				PostChargeSoCPct: next.StartSoC, Capped: true}
		}
		b, err := json.Marshal(day)
		if err != nil {
			return nil, err
		}
		if i < len(shapes) && shapes[i] != "" {
			if b, err = withRouteShape(b, shapes[i]); err != nil {
				return nil, err
			}
		}
		out[i] = b
		total.DistanceMeters += r.TotalDriveDistanceMeters
		total.DriveDurationSec += r.TotalDriveDurationSec
		total.ChargingDurationSec += r.TotalChargingDurationSec
	}
	home, hotel := days[0].Origin, days[0].Destination
	request := map[string]any{
		"start_at":                   stamp(days[0].DepartAt),
		"tz":                         loc.String(),
		"drive_mode":                 "EVERYDAY",
		"has_adapter":                true,
		"target_arrival_soc_percent": days[0].TargetSoC,
		"origin":                     map[string]any{"lat": home.Lat, "lon": home.Lon, "label": home.Label},
		"days": []any{
			map[string]any{"end": map[string]any{"lat": hotel.Lat, "lon": hotel.Lon, "label": rivian.SampleHotelLabel},
				"overnight": map[string]any{"l2_kw": rivian.SampleHotelKW, "until": days[1].DepartAt.In(loc).Format("15:04"),
					"max_soc_pct": rivian.SampleOvernightMaxSoC, "name": rivian.SampleHotelLabel}},
			map[string]any{"end": map[string]any{"lat": home.Lat, "lon": home.Lon, "label": home.Label}, "depart_at": stamp(days[1].DepartAt)},
		},
		"max_overnight_soc_pct": rivian.SampleOvernightMaxSoC,
	}
	return json.Marshal(map[string]any{"kind": "multiday", "request": request, "days": out, "total": total})
}

// withRouteShape sets route_polyline on the first route of a day's plan.
func withRouteShape(day json.RawMessage, shape string) (json.RawMessage, error) {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(day, &d); err != nil {
		return nil, err
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(d["plan"], &plan); err != nil {
		return nil, err
	}
	var routes []map[string]any
	if err := json.Unmarshal(plan["Routes"], &routes); err != nil || len(routes) == 0 {
		return nil, fmt.Errorf("demo: day plan has no route")
	}
	routes[0]["route_polyline"] = shape
	var err error
	if plan["Routes"], err = json.Marshal(routes); err != nil {
		return nil, err
	}
	if d["plan"], err = json.Marshal(plan); err != nil {
		return nil, err
	}
	return json.Marshal(d)
}

// plannerInputs is the planner form (SavedTripInputs) for a trip; a
// zero depart leaves the departure unset.
func plannerInputs(from, to rivian.SamplePlace, startSoC, targetSoC float64, depart time.Time) (json.RawMessage, error) {
	in := map[string]any{
		"origin":       map[string]any{"lat": from.Lat, "lon": from.Lon, "label": from.Label},
		"destination":  map[string]any{"lat": to.Lat, "lon": to.Lon, "label": to.Label},
		"extra_stops":  []any{},
		"target_soc":   strconv.FormatFloat(targetSoC, 'f', -1, 64),
		"starting_soc": strconv.FormatFloat(startSoC, 'f', -1, 64),
		"drive_mode":   "EVERYDAY",
		"has_adapter":  true,
		"departure_at": "",
	}
	if !depart.IsZero() {
		loc, err := time.LoadLocation(planLocation)
		if err != nil {
			return nil, fmt.Errorf("demo: plan location: %w", err)
		}
		in["departure_at"] = depart.In(loc).Format("2006-01-02T15:04")
	}
	return json.Marshal(in)
}

// plannerResponse is plan as POST /api/trips/plan returns it: the
// TripPlan plus its routes' costs, priced as that handler prices them.
func plannerResponse(plan rivian.TripPlan, packKWh float64, cfg settings.ChargingConfig, nets []settings.ChargingNetwork) (json.RawMessage, error) {
	tc := tripadvice.Context{PackKWh: packKWh, HomePricePerKWh: cfg.HomePricePerKWh, HomeCurrency: cfg.HomeCurrency,
		GasPricePerGallon: cfg.GasPricePerGallon, ComparisonMPG: cfg.ComparisonMPG, DCFCNetworks: settings.AsOverrides(nets)}
	costs := make([]tripadvice.CostEstimate, len(plan.Routes))
	for i := range plan.Routes {
		costs[i] = tripadvice.EstimateRoute(&plan.Routes[i], tc)
	}
	return json.Marshal(struct {
		*rivian.TripPlan
		Costs []tripadvice.CostEstimate `json:"costs,omitempty"`
	}{&plan, costs})
}

// SeedPlans writes the demo's saved plans for now, and returns how
// many rows it changed. Idempotent: a row is rewritten only when its
// plan or inputs changed (the next loop's trip, or new settings).
func SeedPlans(ctx context.Context, d *sql.DB, sf *settings.Factory, now time.Time) (int, error) {
	if !Enabled() {
		return 0, nil
	}
	st := sf.For(UserID())
	cfg, err := settings.GetChargingConfig(ctx, st)
	if err != nil {
		return 0, fmt.Errorf("demo: charging config: %w", err)
	}
	nets, err := settings.GetChargingNetworks(ctx, st)
	if err != nil {
		return 0, fmt.Errorf("demo: charging networks: %w", err)
	}
	// The plans pair with a trip the replay has recorded whole: the
	// backfill can start mid-trip on a fresh install.
	var since sql.NullTime
	if err := d.QueryRowContext(ctx, `
		SELECT min(vs.at) FROM vehicle_state vs
		  JOIN vehicles v ON v.id = vs.vehicle_id
		 WHERE v.user_id = $1 AND v.rivian_vehicle_id = $2`, UserID(), VehicleID()).Scan(&since); err != nil {
		return 0, fmt.Errorf("demo: first recorded frame: %w", err)
	}
	if !since.Valid {
		return 0, nil // nothing recorded yet
	}
	plans, err := SamplePlans(now, since.Time, cfg, nets)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range plans {
		res, err := d.ExecContext(ctx, `
			INSERT INTO saved_trips (id, user_id, name, inputs, plan, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)
			ON CONFLICT (user_id, name) DO UPDATE
			   SET id = EXCLUDED.id, inputs = EXCLUDED.inputs, plan = EXCLUDED.plan,
			       created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at
			 WHERE saved_trips.id <> EXCLUDED.id
			    OR saved_trips.inputs IS DISTINCT FROM EXCLUDED.inputs
			    OR saved_trips.plan IS DISTINCT FROM EXCLUDED.plan`,
			p.ID, UserID(), p.Name, []byte(p.Inputs), []byte(p.Plan), p.SavedAt.UTC())
		if err != nil {
			return n, fmt.Errorf("demo: saved trip %q: %w", p.Name, err)
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, nil
}

// RunPlans seeds the saved plans now and then every interval until ctx
// ends, so they follow the replay onto each loop's trip.
func RunPlans(ctx context.Context, d *sql.DB, sf *settings.Factory, every time.Duration, log func(msg string, args ...any)) {
	tick := func() {
		if n, err := SeedPlans(ctx, d, sf, time.Now()); err != nil {
			log("demo: saved plan pass failed", "err", err.Error())
		} else if n > 0 {
			log("demo: saved plans updated", "rows", n)
		}
	}
	tick()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
