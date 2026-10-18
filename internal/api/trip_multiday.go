package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripmultiday"
	"github.com/apohor/rivolt/internal/weather"
)

// tripPoint is a place in a plan request. The coordinates may be written
// lat/lon or latitude/longitude (the single-day waypoint spelling).
type tripPoint struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     string  `json:"label,omitempty"`
	EntityID  string  `json:"entity_id,omitempty"`
}

func (p tripPoint) waypoint() tripmultiday.Waypoint {
	lat, lon := p.Lat, p.Lon
	if lat == 0 && lon == 0 {
		lat, lon = p.Latitude, p.Longitude
	}
	return tripmultiday.Waypoint{Latitude: lat, Longitude: lon, Label: p.Label, EntityID: p.EntityID}
}

// tripPlanMultidayRequest is the SPA-facing input. Everything about the truck
// (vehicle, starting SoC, pack size) is optional and filled in the way the
// single-day handler does it.
//
// A trip is an origin and a list of days; day k's start is day k-1's end. The
// older shape (destination plus overnights) is still accepted and is mapped
// onto days with no vias.
type tripPlanMultidayRequest struct {
	VehicleID               *string               `json:"vehicle_id"`
	StartingSoC             *float64              `json:"starting_soc"`
	PackKWh                 float64               `json:"pack_kwh"`
	StartAt                 string                `json:"start_at"`
	TZ                      string                `json:"tz"`
	DriveMode               string                `json:"drive_mode,omitempty"`
	HasAdapter              *bool                 `json:"has_adapter,omitempty"`
	TrailerProfile          string                `json:"trailer_profile,omitempty"`
	TargetArrivalSocPercent *float64              `json:"target_arrival_soc_percent,omitempty"`
	NetworkPreferences      []tripPlanNetworkPref `json:"network_preferences,omitempty"`
	Origin                  tripPoint             `json:"origin"`
	Days                    []tripPlanDayRequest  `json:"days"`
	MaxOvernightSoCPct      float64               `json:"max_overnight_soc_pct"`

	// Legacy shape.
	Destination tripPoint                   `json:"destination"`
	Overnights  []tripPlanMultidayOvernight `json:"overnights"`
}

type tripPlanDayRequest struct {
	Vias                []tripPoint        `json:"vias"`
	End                 tripPoint          `json:"end"`
	DepartAt            string             `json:"depart_at"`
	TargetArrivalSocPct *float64           `json:"target_arrival_soc_pct"`
	Overnight           *tripPlanOvernight `json:"overnight"`
}

type tripPlanOvernight struct {
	L2KW      *float64 `json:"l2_kw"`
	Until     string   `json:"until"`
	MaxSoCPct float64  `json:"max_soc_pct"`
	Name      string   `json:"name"`
}

type tripPlanMultidayOvernight struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	EntityID    string  `json:"entity_id,omitempty"`
	Name        string  `json:"name,omitempty"`
	ParkedHours float64 `json:"parked_hours,omitempty"`
	L2KW        float64 `json:"l2_kw,omitempty"`
}

// tripPlanMultidayResponse is the wire shape. Each day mirrors what
// /api/trips/plan returns for a single trip, plus its times, endpoints and
// overnight; the SPA renders one card per day.
type tripPlanMultidayResponse struct {
	Days      []tripPlanMultidayDay `json:"days"`
	Total     tripmultiday.Totals   `json:"total"`
	Simulated bool                  `json:"simulated,omitempty"`
	// FailedDay is the day that could not be planned; the days before it are
	// complete. Error, ErrorClass and ErrorReason say why.
	FailedDay   int    `json:"failed_day,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorClass  string `json:"error_class,omitempty"`
	ErrorReason string `json:"error_reason,omitempty"`
}

type tripPlanMultidayDay struct {
	tripmultiday.Day
	WeatherAdjustment *tripWeatherAdjustmentDTO `json:"weather_adjustment,omitempty"`
	Costs             []tripadvice.CostEstimate `json:"costs,omitempty"`
}

// handleTripPlanMultiday wraps the tripmultiday orchestrator.
func handleTripPlanMultiday(c rivian.Client, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID, mgr *settings.Manager, settingsStore *settings.Store, wc *weather.Client, mc *weather.MemCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planner, ok := tripPlannerOf(c)
		if !ok {
			planError(w, http.StatusNotFound, "live rivian client required")
			return
		}
		var req tripPlanMultidayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			badRequest(w, "invalid body: "+err.Error())
			return
		}
		days, err := req.dayInputs()
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		if len(days) == 0 {
			badRequest(w, "at least one day required (use /api/trips/plan for a single day)")
			return
		}
		var startAt time.Time
		if req.StartAt != "" {
			if startAt, err = time.Parse(time.RFC3339, req.StartAt); err != nil {
				badRequest(w, "start_at must be an RFC 3339 time")
				return
			}
		}
		loc := time.UTC
		if req.TZ != "" {
			if loc, err = time.LoadLocation(req.TZ); err != nil {
				badRequest(w, "tz must be an IANA zone such as America/Chicago")
				return
			}
		}

		vid := ""
		if req.VehicleID != nil {
			vid = *req.VehicleID
		}
		rv, msg := resolveVehicle(r.Context(), mon, pool, uid, vid, req.StartingSoC, 0)
		if msg != "" {
			badRequest(w, msg)
			return
		}
		pack := req.PackKWh
		if pack <= 0 {
			pack = vehiclePackKWh(r.Context(), pool, uid, rv.VehicleID)
		}
		if pack <= 0 {
			badRequest(w, "pack_kwh required (this vehicle's pack size is not known)")
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
			VehicleID:           rv.VehicleID,
			HasAdapter:          req.HasAdapter,
			DriveMode:           drive,
			TrailerProfile:      req.TrailerProfile,
			PackKWh:             pack,
			StartingSoC:         rv.StartingSoC,
			StartAt:             startAt,
			Loc:                 loc,
			Origin:              req.Origin.waypoint(),
			Days:                days,
			MaxOvernightSoCPct:  req.MaxOvernightSoCPct,
			TargetArrivalSoCPct: req.TargetArrivalSocPercent,
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

		out, err := tripmultiday.Plan(r.Context(), planner, mreq)
		if err != nil {
			var re *tripmultiday.RequestError
			if errors.As(err, &re) {
				badRequest(w, re.Error())
				return
			}
			slog.WarnContext(r.Context(), "multi-day plan failed", "vehicle_id", rv.VehicleID, "days", len(days), "err", err.Error())
			writeUpstreamError(w, err)
			return
		}

		// The same weather, profile and cost pass the single-day plan gets,
		// run on each day from its own starting SoC and reserve.
		enr := &planEnricher{pool: pool, uid: uid, mgr: mgr, settingsStore: settingsStore, wc: wc, mc: mc, vehicleID: rv.VehicleID, packKWh: pack}
		enr.load(r.Context())
		resp := tripPlanMultidayResponse{Total: out.Total, Simulated: out.Simulated, Days: make([]tripPlanMultidayDay, 0, len(out.Days))}
		for i, d := range out.Days {
			day := tripPlanMultidayDay{Day: d}
			target := req.TargetArrivalSocPercent
			if req.Days != nil && i < len(req.Days) && req.Days[i].TargetArrivalSocPct != nil {
				target = req.Days[i].TargetArrivalSocPct
			}
			day.WeatherAdjustment, day.Costs = enr.enrich(r.Context(), d.Plan, d.DepartureSoC, target)
			resp.Days = append(resp.Days, day)
		}
		if out.FailedDay > 0 {
			resp.FailedDay, resp.Error = out.FailedDay, out.Error
			if out.FailedErr != nil {
				_, body := upstreamErrorBody(out.FailedErr)
				resp.ErrorClass, _ = body["class"].(string)
				resp.ErrorReason, _ = body["reason"].(string)
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// dayInputs turns the request's days, or the older destination-plus-overnights
// shape, into orchestrator days.
func (req *tripPlanMultidayRequest) dayInputs() ([]tripmultiday.DayInput, error) {
	if len(req.Days) > 0 {
		out := make([]tripmultiday.DayInput, len(req.Days))
		for i, d := range req.Days {
			di := tripmultiday.DayInput{End: d.End.waypoint(), TargetArrivalSoCPct: d.TargetArrivalSocPct}
			for _, v := range d.Vias {
				di.Vias = append(di.Vias, v.waypoint())
			}
			if d.DepartAt != "" {
				t, err := time.Parse(time.RFC3339, d.DepartAt)
				if err != nil {
					return nil, errors.New("day " + itoa(i+1) + ": depart_at must be an RFC 3339 time")
				}
				di.DepartAt = &t
			}
			if o := d.Overnight; o != nil {
				di.Overnight = &tripmultiday.Overnight{L2KW: o.L2KW, Until: o.Until, MaxSoCPct: o.MaxSoCPct, Name: o.Name}
			}
			out[i] = di
		}
		return out, nil
	}
	if len(req.Overnights) == 0 {
		return nil, nil
	}
	out := make([]tripmultiday.DayInput, 0, len(req.Overnights)+1)
	for _, ov := range req.Overnights {
		l2 := ov.L2KW // an unset l2_kw was always "no charging" in this shape
		out = append(out, tripmultiday.DayInput{
			End:       tripmultiday.Waypoint{Latitude: ov.Latitude, Longitude: ov.Longitude, EntityID: ov.EntityID, Label: ov.Name},
			Overnight: &tripmultiday.Overnight{L2KW: &l2, ParkedHours: ov.ParkedHours, Name: ov.Name},
		})
	}
	out = append(out, tripmultiday.DayInput{End: req.Destination.waypoint()})
	return out, nil
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
