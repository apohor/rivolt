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
// (same rows, same ids) when the plan for now differs from the stored
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

// SamplePlans returns the demo's saved plans for now, priced with the
// demo user's charging settings (cfg, nets) as /api/trips/plan prices a
// plan. Nil before the first sample road trip is over.
func SamplePlans(now time.Time, cfg settings.ChargingConfig, nets []settings.ChargingNetwork) ([]SamplePlan, error) {
	loc, err := time.LoadLocation(planLocation)
	if err != nil {
		return nil, fmt.Errorf("demo: plan location: %w", err)
	}
	var out []SamplePlan
	for _, p := range rivian.SampleTripPlans(now) {
		in := map[string]any{
			"origin":       map[string]any{"lat": p.Origin.Lat, "lon": p.Origin.Lon, "label": p.Origin.Label},
			"destination":  map[string]any{"lat": p.Destination.Lat, "lon": p.Destination.Lon, "label": p.Destination.Label},
			"extra_stops":  []any{},
			"target_soc":   strconv.FormatFloat(p.TargetSoC, 'f', -1, 64),
			"starting_soc": strconv.FormatFloat(p.StartSoC, 'f', -1, 64),
			"drive_mode":   "EVERYDAY",
			"has_adapter":  true,
			"departure_at": p.DepartAt.In(loc).Format("2006-01-02T15:04"),
		}
		tc := tripadvice.Context{PackKWh: p.PackKWh, HomePricePerKWh: cfg.HomePricePerKWh, HomeCurrency: cfg.HomeCurrency,
			GasPricePerGallon: cfg.GasPricePerGallon, ComparisonMPG: cfg.ComparisonMPG, DCFCNetworks: settings.AsOverrides(nets)}
		costs := make([]tripadvice.CostEstimate, len(p.Plan.Routes))
		for i := range p.Plan.Routes {
			costs[i] = tripadvice.EstimateRoute(&p.Plan.Routes[i], tc)
		}
		plan := struct {
			*rivian.TripPlan
			Costs []tripadvice.CostEstimate `json:"costs,omitempty"`
		}{&p.Plan, costs}
		ib, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		pb, err := json.Marshal(plan)
		if err != nil {
			return nil, err
		}
		out = append(out, SamplePlan{Name: p.Name, SavedAt: p.SavedAt, Inputs: ib, Plan: pb, DepartAt: p.DepartAt})
	}
	return out, nil
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
	plans, err := SamplePlans(now, cfg, nets)
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
