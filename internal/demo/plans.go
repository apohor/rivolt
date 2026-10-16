package demo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripadvice"
)

// The demo's saved trip plans (demo parity): the sample owner's plans
// for the Las Vegas trip (out and back), so Plan vs Actual pairs a
// saved plan with the outing the replay drove. The corpus loops, so the
// plans move to each loop's trip once it is over: a pass rewrites them
// (the same rows) when the plan for now differs from the stored
// one.

// planLocation is where the sample owner plans their trips: the
// planner's departure field is local time without a zone.
const planLocation = "America/Los_Angeles"

// SamplePlan is one saved trip as the planner stores it.
type SamplePlan struct {
	Name     string
	SavedAt  time.Time
	Inputs   json.RawMessage // the planner's SavedTripInputs
	Plan     json.RawMessage // rivian.TripPlan plus costs, as /api/trips/plan returns it
	DepartAt time.Time
}

// SampleTripNames are the names of the demo's saved plans; a preset
// trip links to one by name (Preset.SavedTripName).
func SampleTripNames() []string { return rivian.SampleTripPlanNames() }

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
		out = append(out, SamplePlan{Name: p.Name, SavedAt: p.SavedAt, Inputs: ib, Plan: pb, DepartAt: p.DepartAt})
	}
	return out, nil
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
			INSERT INTO saved_trips (user_id, name, inputs, plan, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (user_id, name) DO UPDATE
			   SET inputs = EXCLUDED.inputs, plan = EXCLUDED.plan,
			       created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at
			 WHERE saved_trips.inputs IS DISTINCT FROM EXCLUDED.inputs
			    OR saved_trips.plan IS DISTINCT FROM EXCLUDED.plan`,
			UserID(), p.Name, []byte(p.Inputs), []byte(p.Plan), p.SavedAt.UTC())
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
