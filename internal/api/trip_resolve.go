package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripprofile"
	"github.com/apohor/rivolt/internal/weather"
)

// resolvedVehicle is what a plan request needs to know about the truck.
type resolvedVehicle struct {
	VehicleID   string
	StartingSoC float64
	RangeMeters float64
}

// resolveVehicle fills in the vehicle id, starting SoC and range a request
// left out, preferring the per-pod monitor cache (the hot path), then the
// database when another replica holds the vehicle's lease. A non-empty message
// is a 400. soc may be nil (not given).
func resolveVehicle(ctx context.Context, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID,
	vehicleID string, soc *float64, rangeMeters float64) (resolvedVehicle, string) {
	out := resolvedVehicle{VehicleID: vehicleID, RangeMeters: rangeMeters}
	have := soc != nil
	if have {
		out.StartingSoC = *soc
	}
	if mon != nil && out.VehicleID == "" {
		for _, v := range mon.AllVehicleInfo() {
			out.VehicleID = v.ID
			break
		}
	}
	if mon != nil && !have && out.VehicleID != "" {
		if st, _ := mon.Latest(out.VehicleID); st != nil {
			out.StartingSoC, have = st.BatteryLevelPct, true
			if out.RangeMeters == 0 && st.DistanceToEmpty > 0 {
				out.RangeMeters = st.DistanceToEmpty * 1000
			}
		}
	}
	// DB fallback when the monitor cache didn't cover us: the typical
	// multi-pod case where the other replica holds the lease for this user's
	// vehicle and our cache is empty.
	if out.VehicleID == "" && pool != nil {
		if vs, err := db.ListUserVehicles(ctx, pool, uid); err == nil && len(vs) > 0 {
			out.VehicleID = vs[0].RivianVehicleID
		}
	}
	if out.VehicleID == "" {
		return out, "vehicle_id required (no vehicle linked to this user)"
	}
	if !have && pool != nil {
		var s, rangeMi sql.NullFloat64
		err := pool.QueryRowContext(ctx, `
			SELECT battery_level_pct, range_mi
			  FROM vehicle_state
			 WHERE user_id = $1
			   AND vehicle_id = (
			       SELECT id FROM vehicles
			        WHERE user_id = $1 AND rivian_vehicle_id = $2
			        LIMIT 1)
			 ORDER BY at DESC
			 LIMIT 1`, uid, out.VehicleID).Scan(&s, &rangeMi)
		if err == nil && s.Valid {
			out.StartingSoC, have = s.Float64, true
			if rangeMi.Valid && out.RangeMeters == 0 {
				out.RangeMeters = rangeMi.Float64 * 1609.34 // mi to m
			}
		}
	}
	if !have && out.StartingSoC == 0 {
		return out, "starting_soc required (no recent vehicle_state row for this vehicle)"
	}
	return out, ""
}

// vehiclePackKWh is the stored pack size of the user's vehicle, 0 if unknown.
func vehiclePackKWh(ctx context.Context, pool *sql.DB, uid uuid.UUID, vehicleID string) float64 {
	if pool == nil || vehicleID == "" {
		return 0
	}
	var kwh sql.NullFloat64
	if err := pool.QueryRowContext(ctx,
		`SELECT pack_kwh FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2 LIMIT 1`, uid, vehicleID).Scan(&kwh); err != nil || !kwh.Valid {
		return 0
	}
	return kwh.Float64
}

// planEnricher adds the post-plan corrections shared by the single-day and
// per-day multi-day handlers: the weather and vehicle-profile adjustment, and
// the per-route cost estimate. All of it is best-effort.
type planEnricher struct {
	pool          *sql.DB
	uid           uuid.UUID
	mgr           *settings.Manager
	settingsStore *settings.Store
	wc            *weather.Client
	mc            *weather.MemCache

	vehicleID string
	packKWh   float64

	// Loaded once per request by load.
	profileMult    float64
	profileReasons []tripprofile.Reason
	costs          tripadvice.Context
	costsOn        bool
}

// load reads the vehicle profile and the charging settings once, so a request
// of several days does not repeat them.
func (e *planEnricher) load(ctx context.Context) {
	e.profileMult = 1.0
	if e.pool != nil && e.vehicleID != "" {
		var vid uuid.UUID
		err := e.pool.QueryRowContext(ctx,
			`SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2 LIMIT 1`,
			e.uid, e.vehicleID).Scan(&vid)
		if err == nil {
			if prof, err := db.GetVehicleProfile(ctx, e.pool, e.uid, vid); err == nil {
				e.profileMult = tripprofile.Multiplier(prof)
				e.profileReasons = tripprofile.Reasons(prof)
			}
		}
	}
	if e.packKWh > 0 {
		e.costsOn = true
		e.costs = tripadvice.Context{PackKWh: e.packKWh}
		if e.settingsStore != nil {
			if cfg, err := settings.GetChargingConfig(ctx, e.settingsStore); err == nil {
				e.costs.HomePricePerKWh = cfg.HomePricePerKWh
				e.costs.HomeCurrency = cfg.HomeCurrency
				e.costs.GasPricePerGallon = cfg.GasPricePerGallon
				e.costs.ComparisonMPG = cfg.ComparisonMPG
			}
			if nets, err := settings.GetChargingNetworks(ctx, e.settingsStore); err == nil {
				e.costs.DCFCNetworks = settings.AsOverrides(nets)
			}
		}
	}
}

// enrich returns the weather/profile adjustment for the plan (nil when
// neither has signal) and the per-route costs (nil without a pack size).
func (e *planEnricher) enrich(ctx context.Context, plan *rivian.TripPlan, startingSoC float64, targetArrival *float64) (*tripWeatherAdjustmentDTO, []tripadvice.CostEstimate) {
	var adj *tripWeatherAdjustmentDTO
	weatherOn := e.mgr != nil && e.mgr.RecapWeatherEnabled() && e.wc != nil
	if weatherOn || e.profileMult != 1.0 {
		target := 10.0
		if targetArrival != nil {
			target = *targetArrival
		}
		adj = computeTripWeatherAdjustment(ctx, plan, startingSoC, target, e.wc, e.mc, e.profileMult, e.profileReasons, weatherOn)
	}
	var costs []tripadvice.CostEstimate
	if e.costsOn && plan != nil && len(plan.Routes) > 0 {
		costs = make([]tripadvice.CostEstimate, len(plan.Routes))
		for i := range plan.Routes {
			costs[i] = tripadvice.EstimateRoute(&plan.Routes[i], e.costs)
		}
	}
	return adj, costs
}

// statusBadRequest is a small helper for the handlers that map a resolver
// message to a 400.
func badRequest(w http.ResponseWriter, msg string) { http.Error(w, msg, http.StatusBadRequest) }
