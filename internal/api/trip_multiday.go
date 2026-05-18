package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripmultiday"
)

// tripPlanMultidayRequest is the SPA-facing input. Mirrors
// tripPlanRequest where it can; adds overnights and the SoC cap.
//
// Unlike the single-day handler, this one requires the SPA to send
// vehicle_id / starting_soc / pack_kwh explicitly — v1 keeps the
// multi-day path minimal, leaving auto-fill to the existing
// single-day endpoint.
type tripPlanMultidayRequest struct {
	VehicleID               string                            `json:"vehicle_id"`
	StartingSoC             float64                           `json:"starting_soc"`
	PackKWh                 float64                           `json:"pack_kwh"`
	DriveMode               string                            `json:"drive_mode,omitempty"`
	HasAdapter              *bool                             `json:"has_adapter,omitempty"`
	TargetArrivalSocPercent *float64                          `json:"target_arrival_soc_percent,omitempty"`
	NetworkPreferences      []tripPlanNetworkPref             `json:"network_preferences,omitempty"`
	Origin                  tripPlanWaypoint                  `json:"origin"`
	Destination             tripPlanWaypoint                  `json:"destination"`
	Overnights              []tripPlanMultidayOvernight       `json:"overnights"`
	MaxOvernightSoCPct      float64                           `json:"max_overnight_soc_pct"`
}

type tripPlanMultidayOvernight struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	EntityID    string  `json:"entity_id,omitempty"`
	Name        string  `json:"name,omitempty"`
	ParkedHours float64 `json:"parked_hours,omitempty"`
	L2KW        float64 `json:"l2_kw,omitempty"`
}

// tripPlanMultidayResponse is the wire shape. Each Day mirrors what
// /api/trips/plan returns for a single trip; the SPA renders one
// RouteCard per Day.
type tripPlanMultidayResponse struct {
	Days  []tripPlanMultidayDay  `json:"days"`
	Total tripmultiday.Totals    `json:"total"`
}

type tripPlanMultidayDay struct {
	Index        int                          `json:"index"`
	Plan         *rivian.TripPlan             `json:"plan"`
	DepartureSoC float64                      `json:"departure_soc"`
	ArrivalSoC   float64                      `json:"arrival_soc"`
	Overnight    *tripmultiday.OvernightResult `json:"overnight,omitempty"`
	Costs        []tripadvice.CostEstimate    `json:"costs,omitempty"`
}

// handleTripPlanMultiday wraps the tripmultiday orchestrator.
func handleTripPlanMultiday(c rivian.Client, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var req tripPlanMultidayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.VehicleID == "" || req.StartingSoC <= 0 || req.PackKWh <= 0 {
			http.Error(w, "vehicle_id, starting_soc, pack_kwh all required", http.StatusBadRequest)
			return
		}
		if len(req.Overnights) == 0 {
			http.Error(w, "at least one overnight required (use /api/trips/plan for single-day)", http.StatusBadRequest)
			return
		}

		// Validate drive_mode against the gateway's strict enum, same
		// filter the single-day handler applies.
		drive := ""
		switch req.DriveMode {
		case "", settings.DriveModeEveryday, settings.DriveModeDistance,
			settings.DriveModeSport, settings.DriveModeWinter,
			settings.DriveModeOffRoadAuto:
			drive = req.DriveMode
		}

		mreq := tripmultiday.Request{
			VehicleID:           req.VehicleID,
			HasAdapter:          req.HasAdapter,
			DriveMode:           drive,
			PackKWh:             req.PackKWh,
			StartingSoC:         req.StartingSoC,
			Origin:              tripmultiday.Waypoint{Latitude: req.Origin.Latitude, Longitude: req.Origin.Longitude, EntityID: req.Origin.EntityID},
			Destination:         tripmultiday.Waypoint{Latitude: req.Destination.Latitude, Longitude: req.Destination.Longitude, EntityID: req.Destination.EntityID},
			MaxOvernightSoCPct:  req.MaxOvernightSoCPct,
			TargetArrivalSoCPct: req.TargetArrivalSocPercent,
		}
		for _, ov := range req.Overnights {
			mreq.Overnights = append(mreq.Overnights, tripmultiday.OvernightStop{
				Waypoint:    tripmultiday.Waypoint{Latitude: ov.Latitude, Longitude: ov.Longitude, EntityID: ov.EntityID},
				Name:        ov.Name,
				ParkedHours: ov.ParkedHours,
				L2KW:        ov.L2KW,
			})
		}
		for _, np := range req.NetworkPreferences {
			mreq.NetworkPreferences = append(mreq.NetworkPreferences, rivian.NetworkPreference{
				NetworkID:  np.NetworkID,
				Preference: np.Preference,
			})
		}
		// Auto-inject the full networkPreferences list (same as the
		// single-day handler). See handleTripPlan for the
		// preference=1/0 hypothesis rationale.
		if settingsStore != nil {
			if nets, err := settings.GetChargingNetworks(r.Context(), settingsStore); err == nil {
				existing := make(map[string]bool, len(mreq.NetworkPreferences))
				for _, np := range mreq.NetworkPreferences {
					existing[np.NetworkID] = true
				}
				for _, p := range settings.NetworkPreferenceList(nets) {
					if existing[p.NetworkID] {
						continue
					}
					mreq.NetworkPreferences = append(mreq.NetworkPreferences, rivian.NetworkPreference{
						NetworkID:  p.NetworkID,
						Preference: p.Preference,
					})
				}
			}
		}

		out, err := tripmultiday.Plan(r.Context(), lc, mreq)
		if err != nil {
			slog.WarnContext(r.Context(), "multi-day plan failed",
				"vehicle_id", req.VehicleID,
				"overnights", len(req.Overnights),
				"err", err.Error())
			writeUpstreamError(w, err)
			return
		}

		// Per-route DCFC cost on every day's routes. Settings are
		// account-wide so we read them once.
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

		resp := tripPlanMultidayResponse{Total: out.Total, Days: make([]tripPlanMultidayDay, 0, len(out.Days))}
		for _, d := range out.Days {
			day := tripPlanMultidayDay{
				Index:        d.Index,
				Plan:         d.Plan,
				DepartureSoC: d.DepartureSoC,
				ArrivalSoC:   d.ArrivalSoC,
				Overnight:    d.Overnight,
			}
			if d.Plan != nil && len(d.Plan.Routes) > 0 {
				day.Costs = make([]tripadvice.CostEstimate, len(d.Plan.Routes))
				for i := range d.Plan.Routes {
					day.Costs[i] = tripadvice.EstimateRoute(&d.Plan.Routes[i], tc)
				}
			}
			resp.Days = append(resp.Days, day)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
