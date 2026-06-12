package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripprofile"
	"github.com/apohor/rivolt/internal/tripweather"
	"github.com/apohor/rivolt/internal/weather"
)

// tripPlanRequest is the SPA-facing input. VehicleID + StartingSoC
// can be omitted; the handler back-fills them from the monitor's
// state cache so callers don't repeat what the server already knows.
type tripPlanRequest struct {
	VehicleID               string                `json:"vehicle_id"`
	StartingSoC             *float64              `json:"starting_soc,omitempty"`
	StartingRangeMeters     float64               `json:"starting_range_meters,omitempty"`
	OriginBearing           float64               `json:"origin_bearing"`
	Waypoints               []tripPlanWaypoint    `json:"waypoints"`
	TargetArrivalSocPercent *float64              `json:"target_arrival_soc_percent,omitempty"`
	DriveMode               string                `json:"drive_mode,omitempty"`
	HasAdapter              *bool                 `json:"has_adapter,omitempty"`
	TrailerProfile          string                `json:"trailer_profile,omitempty"`
	AvoidAdapterRequired    bool                  `json:"avoid_adapter_required,omitempty"`
	SupportedConnectorTypes []string              `json:"supported_connector_types,omitempty"`
	NetworkPreferences      []tripPlanNetworkPref `json:"network_preferences,omitempty"`
	// PackKWh is the vehicle's usable pack capacity. The SPA already
	// has it from /api/vehicles/.../profile; the handler uses it for
	// per-route DCFC cost computation. Zero means "don't price the
	// route" (cost fields stay zero).
	PackKWh float64 `json:"pack_kwh,omitempty"`
}

type tripPlanWaypoint struct {
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	WaypointType string  `json:"waypoint_type"`
	EntityID     string  `json:"entity_id,omitempty"`
}

type tripPlanNetworkPref struct {
	NetworkID  string `json:"network_id"`
	Preference int    `json:"preference"`
}

// handleTripPlan calls Rivian's planTripWithMultiStop. Slice 1 of
// the trip-planner feature: read-only pass-through with no AI, no
// save, no places search. Caller provides waypoint coordinates
// directly (geocoding lands in slice 2).
//
// vehicle_id and starting_soc may be omitted in the body — the
// handler back-fills them, preferring the per-pod monitor cache
// (zero-ms hot path), then falling back to the DB when this pod
// doesn't own the vehicle's lease (multi-pod path: the lease holder
// is a different replica, so this pod's monitor cache is empty for
// that vehicle).
func handleTripPlan(c rivian.Client, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID, mgr *settings.Manager, settingsStore *settings.Store, wc *weather.Client, mc *weather.MemCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var req tripPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Waypoints) < 2 {
			http.Error(w, "at least origin + destination waypoints required", http.StatusBadRequest)
			return
		}
		startingSoC := 0.0
		if req.StartingSoC != nil {
			startingSoC = *req.StartingSoC
		}
		// Prefer the in-memory monitor cache (fastest, freshest).
		if mon != nil && req.VehicleID == "" {
			for _, v := range mon.AllVehicleInfo() {
				req.VehicleID = v.ID
				break
			}
		}
		if mon != nil && req.StartingSoC == nil && req.VehicleID != "" {
			if st, _ := mon.Latest(req.VehicleID); st != nil {
				startingSoC = st.BatteryLevelPct
				req.StartingSoC = &startingSoC
				if req.StartingRangeMeters == 0 && st.DistanceToEmpty > 0 {
					req.StartingRangeMeters = st.DistanceToEmpty * 1000
				}
			}
		}
		// DB fallback when the monitor cache didn't cover us — the
		// typical multi-pod case where the OTHER replica holds the
		// lease for this user's vehicle and our cache is empty.
		if req.VehicleID == "" && pool != nil {
			vs, err := db.ListUserVehicles(r.Context(), pool, uid)
			if err == nil && len(vs) > 0 {
				req.VehicleID = vs[0].RivianVehicleID
			}
		}
		if req.VehicleID == "" {
			http.Error(w, "vehicle_id required (no vehicle linked to this user)", http.StatusBadRequest)
			return
		}
		if req.StartingSoC == nil && pool != nil {
			var soc, rangeMi sql.NullFloat64
			err := pool.QueryRowContext(r.Context(), `
				SELECT battery_level_pct, range_mi
				  FROM vehicle_state
				 WHERE user_id = $1
				   AND vehicle_id = (
				       SELECT id FROM vehicles
				        WHERE user_id = $1 AND rivian_vehicle_id = $2
				        LIMIT 1)
				 ORDER BY at DESC
				 LIMIT 1`, uid, req.VehicleID).Scan(&soc, &rangeMi)
			if err == nil && soc.Valid {
				startingSoC = soc.Float64
				if rangeMi.Valid && req.StartingRangeMeters == 0 {
					req.StartingRangeMeters = rangeMi.Float64 * 1609.34 // mi → m
				}
			}
		}
		if req.StartingSoC == nil && startingSoC == 0 {
			http.Error(w, "starting_soc required (no recent vehicle_state row for this vehicle)", http.StatusBadRequest)
			return
		}

		// Drop unknown drive_mode values before they reach Rivian's
		// GraphQL — the enum is strict and an unknown value fails the
		// whole query. Stale SPA bundles or bad clients can send
		// legacy labels (CONSERVE, ALL_PURPOSE) that aren't in the
		// gateway's enum.
		drive := ""
		switch req.DriveMode {
		case "", settings.DriveModeEveryday, settings.DriveModeDistance,
			settings.DriveModeSport, settings.DriveModeWinter,
			settings.DriveModeOffRoadAuto:
			drive = req.DriveMode
		}
		in := rivian.PlanTripInput{
			VehicleID:               req.VehicleID,
			StartingSoC:             startingSoC,
			StartingRangeMeters:     req.StartingRangeMeters,
			OriginBearing:           req.OriginBearing,
			TargetArrivalSocPercent: req.TargetArrivalSocPercent,
			DriveMode:               drive,
			HasAdapter:              req.HasAdapter,
			TrailerProfile:          req.TrailerProfile,
			AvoidAdapterRequired:    req.AvoidAdapterRequired,
			SupportedConnectorTypes: req.SupportedConnectorTypes,
		}
		for _, wp := range req.Waypoints {
			in.Waypoints = append(in.Waypoints, rivian.PlanTripWaypoint{
				Latitude:     wp.Latitude,
				Longitude:    wp.Longitude,
				WaypointType: wp.WaypointType,
				EntityID:     wp.EntityID,
			})
		}
		for _, np := range req.NetworkPreferences {
			in.NetworkPreferences = append(in.NetworkPreferences, rivian.NetworkPreference{
				NetworkID:  np.NetworkID,
				Preference: np.Preference,
			})
		}
		// Auto-inject the full networkPreferences list from Settings →
		// Charging networks. Working hypothesis: Rivian's gateway
		// expects every known networkId in every request, with
		// preference=1 on the user's picks and preference=0 on the
		// rest (omitting an ID is interpreted as "no opinion", not
		// "deprioritise"). networkId values are the assumed
		// 10001-10009 sequence in dcfcrates.Networks. If the user
		// has no Preferred toggles set, NetworkPreferenceList returns
		// nil and we omit the field entirely.
		if settingsStore != nil {
			if nets, err := settings.GetChargingNetworks(r.Context(), settingsStore); err == nil {
				existing := make(map[string]bool, len(in.NetworkPreferences))
				for _, np := range in.NetworkPreferences {
					existing[np.NetworkID] = true
				}
				for _, p := range settings.NetworkPreferenceList(nets) {
					if existing[p.NetworkID] {
						continue
					}
					in.NetworkPreferences = append(in.NetworkPreferences, rivian.NetworkPreference{
						NetworkID:  p.NetworkID,
						Preference: p.Preference,
					})
				}
			}
		}

		plan, err := lc.PlanTrip(r.Context(), in)
		if err != nil {
			slog.WarnContext(r.Context(), "trip plan failed",
				"vehicle_id", in.VehicleID,
				"waypoints", len(in.Waypoints),
				"err", err.Error())
			// Map upstream error class to an HTTP status the SPA
			// can render. 4xx passes through Cloudflare cleanly;
			// 5xx gets replaced with Cloudflare's HTML error page.
			writeUpstreamError(w, err)
			return
		}
		resp := tripPlanResponse{TripPlan: plan}
		// Resolve the user's saved vehicle profile so we can apply a
		// trip-wide energy multiplier (wheel size, tire type,
		// accessories, payload). Rivian's planTrip2 doesn't accept
		// these inputs, so the correction lives client-side on top
		// of the response. Profile is loaded best-effort: missing
		// rows / lookup errors fall through to multiplier=1.0.
		profileMult := 1.0
		var profileReasons []tripprofile.Reason
		if pool != nil && req.VehicleID != "" {
			var vid uuid.UUID
			err := pool.QueryRowContext(r.Context(),
				`SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2 LIMIT 1`,
				uid, req.VehicleID,
			).Scan(&vid)
			if err == nil {
				if prof, err := db.GetVehicleProfile(r.Context(), pool, uid, vid); err == nil {
					profileMult = tripprofile.Multiplier(prof)
					profileReasons = tripprofile.Reasons(prof)
				}
			}
		}
		// Either weather or profile (or both) can contribute. Fire
		// the adjustment pipeline whenever either has signal.
		weatherOn := mgr != nil && mgr.RecapWeatherEnabled() && wc != nil
		hasProfileSignal := profileMult != 1.0
		if weatherOn || hasProfileSignal {
			target := 10.0
			if in.TargetArrivalSocPercent != nil {
				target = *in.TargetArrivalSocPercent
			}
			resp.WeatherAdjustment = computeTripWeatherAdjustment(r.Context(), plan, in.StartingSoC, target, wc, mc, profileMult, profileReasons, weatherOn)
		}
		// Always-on per-route DCFC cost. Needs pack_kwh from the SPA;
		// without it we just don't price (cost fields stay zero on
		// every route).
		if req.PackKWh > 0 && len(plan.Routes) > 0 {
			tc := tripadvice.Context{PackKWh: req.PackKWh}
			if settingsStore != nil {
				if cfg, err := settings.GetChargingConfig(r.Context(), settingsStore); err == nil {
					tc.HomePricePerKWh = cfg.HomePricePerKWh
					tc.HomeCurrency = cfg.HomeCurrency
					tc.GasPricePerGallon = cfg.GasPricePerGallon
					tc.ComparisonMPG = cfg.ComparisonMPG
				}
				if nets, err := settings.GetChargingNetworks(r.Context(), settingsStore); err == nil {
					tc.DCFCNetworks = settings.AsOverrides(nets)
				}
			}
			resp.Costs = make([]tripadvice.CostEstimate, len(plan.Routes))
			for i := range plan.Routes {
				resp.Costs[i] = tripadvice.EstimateRoute(&plan.Routes[i], tc)
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// tripPlanResponse extends Rivian's plan with our post-correction
// payload. The embedded pointer means the SPA sees the same flat
// JSON shape it always did plus a sibling weather_adjustment field;
// older SPA bundles ignore the new field cleanly.
type tripPlanResponse struct {
	*rivian.TripPlan
	WeatherAdjustment *tripWeatherAdjustmentDTO `json:"weather_adjustment,omitempty"`
	// Costs is aligned with TripPlan.Routes - Costs[i] is the
	// DCFC + home-equivalent estimate for Routes[i]. Empty when the
	// caller didn't pass pack_kwh; the SPA renders the route table
	// without a $ column in that case.
	Costs []tripadvice.CostEstimate `json:"costs,omitempty"`
}

// tripWeatherAdjustmentDTO is the wire shape. Per-leg metadata lets
// the SPA explain *why* the arrival SoC moved (cold, wind, wet,
// plus profile factors) rather than just showing the corrected
// number. ProfileReasons is trip-wide (wheels/tires/accessories/
// payload don't vary by leg) and surfaces in the chip's reason
// list alongside the weather factors.
type tripWeatherAdjustmentDTO struct {
	AdjustedArrivalSoC []float64              `json:"adjusted_arrival_soc"`
	FinalArrivalSoC    float64                `json:"final_arrival_soc"`
	BelowTarget        bool                   `json:"below_target"`
	TargetArrivalSoC   float64                `json:"target_arrival_soc"`
	Legs               []tripWeatherAdjLegDTO `json:"legs"`
	ProfileMultiplier  float64                `json:"profile_multiplier,omitempty"`
	ProfileReasons     []string               `json:"profile_reasons,omitempty"`
}

type tripWeatherAdjLegDTO struct {
	Multiplier  float64  `json:"multiplier"`
	TempC       *float64 `json:"temp_c,omitempty"`
	HeadwindKPH *float64 `json:"headwind_kph,omitempty"`
	PrecipMM    *float64 `json:"precip_mm,omitempty"`
}

// computeTripWeatherAdjustment runs FetchHourCached for each leg in
// parallel (3s budget, best-effort), then folds the snapshots through
// tripweather.Adjust. Returns nil if the plan has no usable route or
// nothing usable came back from upstream — caller renders the plan
// without the chip in that case.
func computeTripWeatherAdjustment(
	ctx context.Context,
	plan *rivian.TripPlan,
	startingSoC, targetArrivalSoC float64,
	wc *weather.Client,
	mc *weather.MemCache,
	profileMult float64,
	profileReasons []tripprofile.Reason,
	weatherOn bool,
) *tripWeatherAdjustmentDTO {
	if plan == nil || len(plan.Routes) == 0 {
		return nil
	}
	wps := plan.Routes[0].Waypoints
	if len(wps) < 2 {
		return nil
	}
	// Drop the origin entry — arrivals start at waypoint index 1.
	type legCtx struct {
		lat, lon, bearing float64
		hasBearing        bool
		at                time.Time
	}
	legs := make([]legCtx, 0, len(wps)-1)
	arrivals := make([]float64, 0, len(wps)-1)
	for i := 1; i < len(wps); i++ {
		from := wps[i-1]
		to := wps[i]
		midLat := (from.Latitude + to.Latitude) / 2
		midLon := (from.Longitude + to.Longitude) / 2
		bearing := weather.Bearing(from.Latitude, from.Longitude, to.Latitude, to.Longitude)
		// Leg ETA = the arrival timestamp; falls back to now if Rivian
		// omitted it (older response shapes).
		at := time.Now().UTC()
		if to.ArrivalTimeUTC != "" {
			if parsed, err := time.Parse(time.RFC3339, to.ArrivalTimeUTC); err == nil {
				at = parsed
			}
		}
		legs = append(legs, legCtx{
			lat: midLat, lon: midLon,
			bearing: bearing, hasBearing: true,
			at: at,
		})
		arrivals = append(arrivals, to.ArrivalSoC)
	}

	// Parallel fetch with a 3s budget - Rivian's plan already cost
	// 1-2s and we don't want to compound. Per-leg fetches are
	// best-effort: a fail on one leg lets that leg fall through to
	// a 1.0 multiplier rather than blocking the whole response.
	// Skipped entirely when the operator hasn't enabled weather -
	// then the adjustment is profile-only.
	snaps := make([]*weather.Snapshot, len(legs))
	if weatherOn {
		fctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for i := range legs {
			i := i
			l := legs[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, _, err := mc.FetchHourCached(fctx, wc, l.lat, l.lon, l.at, l.bearing, l.hasBearing)
				if err == nil {
					snaps[i] = s
				}
			}()
		}
		wg.Wait()
	}

	twLegs := make([]tripweather.Leg, len(legs))
	for i, s := range snaps {
		twLegs[i] = tripweather.Leg{Weather: s}
	}
	if profileMult <= 0 {
		profileMult = 1.0
	}
	adj := tripweather.Adjust(startingSoC, arrivals, twLegs, targetArrivalSoC, profileMult)
	if adj == nil {
		return nil
	}
	dto := &tripWeatherAdjustmentDTO{
		AdjustedArrivalSoC: adj.AdjustedArrivalSoC,
		FinalArrivalSoC:    adj.FinalArrivalSoC,
		BelowTarget:        adj.BelowTarget,
		TargetArrivalSoC:   targetArrivalSoC,
		Legs:               make([]tripWeatherAdjLegDTO, len(adj.Multipliers)),
	}
	if profileMult != 1.0 {
		dto.ProfileMultiplier = profileMult
		labels := make([]string, 0, len(profileReasons))
		for _, r := range profileReasons {
			if r.Label != "" {
				labels = append(labels, r.Label)
			}
		}
		dto.ProfileReasons = labels
	}
	for i, m := range adj.Multipliers {
		leg := tripWeatherAdjLegDTO{Multiplier: m}
		if s := snaps[i]; s != nil {
			if s.HasApparent {
				v := s.ApparentTempC
				leg.TempC = &v
			} else if s.HasTemp {
				v := s.TempC
				leg.TempC = &v
			}
			if s.HasHeadwind {
				v := s.HeadwindKPH
				leg.HeadwindKPH = &v
			}
			if s.HasPrecip {
				v := s.PrecipMM
				leg.PrecipMM = &v
			}
		}
		dto.Legs[i] = leg
	}
	return dto
}

// handleTripPlanAdvice takes a completed TripPlan (returned by
// /api/trips/plan) plus minimal context labels, calls the configured
// AI provider, and returns a short structured analysis: headline +
// 2-4 plain-language insights. Lives in the AI-bound route group so
// the 5-minute timeout applies.
func handleTripPlanAdvice(mgr *settings.Manager, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil || mgr.Analyzer() == nil {
			http.Error(w, "AI provider not configured", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Plan              *rivian.TripPlan `json:"plan"`
			Origin            string           `json:"origin"`
			Destination       string           `json:"destination"`
			DriveMode         string           `json:"drive_mode"`
			StartingSoC       float64          `json:"starting_soc"`
			HasAdapter        bool             `json:"has_adapter"`
			TireFLBar         float64          `json:"tire_fl_bar"`
			TireFRBar         float64          `json:"tire_fr_bar"`
			TireRLBar         float64          `json:"tire_rl_bar"`
			TireRRBar         float64          `json:"tire_rr_bar"`
			TirePlacardPSI    float64          `json:"tire_placard_psi"`
			PackKWh           float64          `json:"pack_kwh"`
			DepartureDatetime string           `json:"departure_datetime"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Plan == nil {
			http.Error(w, "plan required", http.StatusBadRequest)
			return
		}
		tc := tripadvice.Context{
			OriginLabel:      body.Origin,
			DestinationLabel: body.Destination,
			DriveMode:        body.DriveMode,
			StartingSoC:      body.StartingSoC,
			HasAdapter:       body.HasAdapter,
			TirePressureBars: [4]float64{body.TireFLBar, body.TireFRBar, body.TireRLBar, body.TireRRBar},
			TirePlacardPSI:   body.TirePlacardPSI,
			PackKWh:          body.PackKWh,
		}
		// Pull the user's at-home charging rate so the cost section
		// of the advice can quote real numbers, not assumptions.
		if settingsStore != nil {
			if cfg, err := settings.GetChargingConfig(r.Context(), settingsStore); err == nil {
				tc.HomePricePerKWh = cfg.HomePricePerKWh
				tc.HomeCurrency = cfg.HomeCurrency
				tc.GasPricePerGallon = cfg.GasPricePerGallon
				tc.ComparisonMPG = cfg.ComparisonMPG
			}
			// Same source feeds the per-stop DCFC pricing. Empty list
			// (or a settings.GetChargingNetworks error) lets the
			// resolver fall back to the built-in defaults.
			if nets, err := settings.GetChargingNetworks(r.Context(), settingsStore); err == nil {
				tc.DCFCNetworks = settings.AsOverrides(nets)
			}
		}
		// Fetch weather at the origin when the operator has enabled the
		// weather feature. Use the user-supplied departure datetime when
		// present so a future plan gets a forecast for that hour, not now.
		// Best-effort: a failure just omits the context.
		if mgr.RecapWeatherEnabled() {
			if lat, lon, ok := originLatLon(body.Plan); ok {
				at := time.Now()
				if body.DepartureDatetime != "" {
					if t, err := time.Parse(time.RFC3339, body.DepartureDatetime); err == nil {
						at = t
					}
				}
				if snap, _, err := weather.NewClient().FetchHour(r.Context(), lat, lon, at, 0, false); err == nil {
					tc.Weather = snap
				}
			}
		}
		result, err := tripadvice.Generate(r.Context(), mgr.Analyzer(), body.Plan, tc)
		if err != nil {
			slog.WarnContext(r.Context(), "trip advice generation failed", "err", err.Error())
			http.Error(w, "AI analysis failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		type response struct {
			Headline   string                  `json:"headline"`
			Cost       []string                `json:"cost"`
			Efficiency []string                `json:"efficiency"`
			Weather    []string                `json:"weather"`
			Vehicle    []string                `json:"vehicle"`
			CostEst    tripadvice.CostEstimate `json:"cost_estimate"`
			Model      string                  `json:"model"`
		}
		nonNil := func(s []string) []string {
			if s == nil {
				return []string{}
			}
			return s
		}
		var resp response
		resp.Model = result.Model
		resp.CostEst = result.Cost
		if result.Parsed != nil {
			resp.Headline = result.Parsed.Headline
			resp.Cost = nonNil(result.Parsed.Cost)
			resp.Efficiency = nonNil(result.Parsed.Efficiency)
			resp.Weather = nonNil(result.Parsed.Weather)
			resp.Vehicle = nonNil(result.Parsed.Vehicle)
		} else {
			resp.Cost = []string{}
			resp.Efficiency = []string{}
			resp.Weather = []string{}
			resp.Vehicle = []string{}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// originLatLon extracts the lat/lon of the origin waypoint from the
// first route in a plan. Returns ok=false when no origin is found.
func originLatLon(plan *rivian.TripPlan) (lat, lon float64, ok bool) {
	if plan == nil {
		return 0, 0, false
	}
	for _, r := range plan.Routes {
		for _, w := range r.Waypoints {
			if strings.EqualFold(w.WaypointType, "origin") && (w.Latitude != 0 || w.Longitude != 0) {
				return w.Latitude, w.Longitude, true
			}
		}
	}
	return 0, 0, false
}

// handleTripPlanRawDebug forwards an arbitrary variables JSON to
// Rivian's planTripWithMultiStop and returns the gateway's response
// or error verbatim. Admin-only via the chi.Route("/admin") group
// it lives in. Used to reverse-engineer schema/value mismatches
// without round-tripping through chart bumps.
func handleTripPlanRawDebug(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Variables) == 0 {
			http.Error(w, "variables required (object key 'variables')", http.StatusBadRequest)
			return
		}
		data, err := lc.PlanTripRaw(r.Context(), body.Variables)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleGraphQLRaw posts an arbitrary GraphQL document to the
// gateway. Body: {"operation": "...", "query": "...", "variables": {...}}.
// Returns {data, errors} verbatim from upstream — a 200 here means
// the wire roundtrip happened, errors land in the body. Used to
// reverse-engineer schemas that ban introspection.
func handleGraphQLRaw(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var body struct {
			Operation string         `json:"operation"`
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Query == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}
		data, err := lc.RawGraphQL(r.Context(), body.Operation, body.Query, body.Variables)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleGraphQLIntrospect runs an __type introspection on the
// gateway and returns the response verbatim. Lets us read the exact
// input-object shape the gateway publishes, which is the
// authoritative answer for "what fields does CoordinatesInput
// accept?"
func handleGraphQLIntrospect(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name query parameter required (e.g. ?name=CoordinatesInput)", http.StatusBadRequest)
			return
		}
		data, err := lc.IntrospectInputType(r.Context(), name)
		if err != nil {
			// Log so we can read the upstream error in pod logs.
			// Cloudflare swallows 502 bodies (replaces with its own
			// Bad-gateway HTML), so return 400 with the verbatim
			// message: 4xx passes through, the operator sees the
			// actual GraphQL response.
			slog.WarnContext(r.Context(), "gql introspection failed",
				"name", name, "err", err.Error())
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "name": name})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleGraphQLIntrospectEnum runs an __type introspection for the
// enum subselection. Used to read the value list of an enum type
// (e.g. the real list of Rivian network IDs once we know the enum
// name from the inputFields probe).
func handleGraphQLIntrospectEnum(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name query parameter required (e.g. ?name=ChargingNetwork)", http.StatusBadRequest)
			return
		}
		data, err := lc.IntrospectEnum(r.Context(), name)
		if err != nil {
			slog.WarnContext(r.Context(), "gql enum introspection failed",
				"name", name, "err", err.Error())
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "name": name})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleTripPlanDiag is the mobile-friendly diagnostic endpoint
// for the trip planner. Slimmed down after slice 1 shipped: runs
// one known-good v2 call (operation planTripWithMultiStopV2 / field
// planTrip2) with literal values inlined, plus the introspection
// probe on CoordinatesInput as a sanity check that the gateway is
// answering us. Click in browser bar → paste response to debug.
// Admin-only via the route group.
//
// History (deleted): earlier versions had ~10 variant-axis tests
// (waypointType / driveMode / connector lists / soc-as-fraction)
// + 7-name introspection probe + 7-name input-type probe + the
// v1 `planTrip` and broken `planTripMultiStop` shapes. They served
// their purpose during reverse engineering and are gone now —
// kept here as comments so a future operator can tell what was
// already tried:
//
//	v0.17.118  intro of diag (variants: full + minimal)
//	v0.17.121  v119_* fan-out (10 single-axis variants)
//	v0.17.122  query_axes (response-set + planTrip-legacy probes)
//	v0.17.124  added breaker bypass for diag-only calls
//	v0.17.125  full_spec_with_entityid (Place ID test)
//	v0.17.127  v126_correct_schema (planTrip + origin/destination)
//	v0.17.128  v128_v2_schema (planTrip2 with declared types)
//	v0.17.129  inline + 7 input-type-name probe
//	v0.17.130  inlined query merged into the typed PlanTrip path
func handleTripPlanDiag(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		out := map[string]any{}

		// Known-good v2 inlined call. Returns real plans when the
		// upstream is healthy; an INTERNAL_SERVER_ERROR here means
		// Rivian's planner is degraded again.
		v2Query := `query planTripWithMultiStopV2 {
  planTrip2(
    waypoints: [
      {latitude: 30.5538, longitude: -97.7622},
      {latitude: 32.7767, longitude: -96.797}
    ],
    vehicle: "01-242521064",
    startingSoc: 54.0,
    startingRangeMeters: 270000.0,
    targetArrivalSocPercent: 20.0
  ) {
    status
    plans {
      summary {
        destinationReachable
        socBelowLimitAtDestination
        totalChargeDurationSeconds
        totalDriveDurationSeconds
        totalDriveDistanceMeters
        totalTripDurationSeconds
        arrivalSOCPercent
        arrivalRangeMeters
        arrivalEnergyKwh
      }
      waypoints {
        waypointType
        latitude
        longitude
        arrivalSOCPercent
        departureSOCPercent
        arrivalRangeMeters
        departureRangeMeters
        chargeDurationSeconds
      }
    }
  }
}`
		if data, err := lc.RawGraphQL(r.Context(), "planTripWithMultiStopV2", v2Query, map[string]any{}); err != nil {
			out["plan_trip_v2"] = map[string]any{"error": err.Error()}
		} else {
			out["plan_trip_v2"] = map[string]any{"data": data}
		}

		// Light gateway health check — succeeds when the gateway
		// answers introspection at all (currently always rejects
		// it, GRAPHQL_VALIDATION_FAILED). Useful only as a sentinel
		// in case Rivian re-enables introspection in the future.
		if data, err := lc.IntrospectInputType(r.Context(), "CoordinatesInput"); err != nil {
			out["introspect_sentinel"] = map[string]any{"error": err.Error()}
		} else {
			out["introspect_sentinel"] = map[string]any{"data": data}
		}

		writeJSON(w, http.StatusOK, out)
	}
}
