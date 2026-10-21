package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	RoutePreferences        *tripRoutePrefs       `json:"route_preferences,omitempty"`
	TargetArrivalSocPercent *float64              `json:"target_arrival_soc_percent,omitempty"`
	NetworkPreferences      []tripPlanNetworkPref `json:"network_preferences,omitempty"`
	Origin                  tripPoint             `json:"origin"`
	// OriginFromVehicle plans from where the truck is: the origin becomes its
	// latest position and the starting SoC its latest charge ("re-plan from
	// here"; send only the days that are left). 409 {"class":"no_position"}
	// when there is no recent reading.
	OriginFromVehicle  bool                 `json:"origin_from_vehicle"`
	Days               []tripPlanDayRequest `json:"days"`
	MaxOvernightSoCPct float64              `json:"max_overnight_soc_pct"`

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
	// Hookup marks an own stop: the user chose the connector they will plug into
	// (see hookupRules). It sets the default power and the bounds l2_kw must
	// stay within; without it the stop is a listed charger and l2_kw keeps the
	// older, looser check (the plan caps the charge at the truck's AC maximum).
	Hookup string `json:"hookup,omitempty"`
}

// hookupRule is what an own stop's hookup allows. Power through Rivian's
// Portable Charger is 32 A at most, 7.6 kW (Rivian's Portable Charger page,
// read 2026-10-20), so a NEMA 14-50 stop is planned at 7.2 kW (a little under
// for campground voltage). A 30 A TT-30 or any 120 V outlet is limited by the
// charger's 120 V mode and an adapter, planned at 1.4 kW. A J1772 stop is bounded
// by the R1/R2 on-board AC charger (11.5 kW at most; the plan also limits the
// charge to the truck's own AC maximum).
type hookupRule struct {
	defaultKW, minKW, maxKW float64
}

var hookupRules = map[string]hookupRule{
	"j1772":      {defaultKW: 7.0, minKW: 1.0, maxKW: 11.5},
	"nema_14_50": {defaultKW: 7.2, minKW: 1.0, maxKW: 7.6},
	"tt_30":      {defaultKW: 1.4, minKW: 1.0, maxKW: 1.4},
	"outlet_120": {defaultKW: 1.4, minKW: 1.0, maxKW: 1.4},
	"none":       {defaultKW: 0, minKW: 0, maxKW: 0},
}

// maxOwnStopName is the longest name of an own stop.
const maxOwnStopName = 80

// ownStopErr checks an own stop (an overnight with a hookup): a known hookup,
// a power inside what that hookup allows (rejected, never clamped), and a short
// plain name. Nothing here calls out to a geocoder or the weather service.
func ownStopErr(o *tripPlanOvernight) string {
	if o.Hookup == "" {
		return ""
	}
	r, ok := hookupRules[o.Hookup]
	if !ok {
		return "overnight hookup must be one of j1772, nema_14_50, tt_30, outlet_120, none"
	}
	if o.L2KW != nil {
		kw := *o.L2KW
		if math.IsNaN(kw) || (o.Hookup == "none" && kw != 0) || (o.Hookup != "none" && (kw < r.minKW || kw > r.maxKW)) {
			return "overnight l2_kw for " + o.Hookup + " must be between " + fmtKW(r.minKW) + " and " + fmtKW(r.maxKW) + " kW"
		}
	}
	if utf8.RuneCountInString(o.Name) > maxOwnStopName {
		return "overnight name is longer than 80 characters"
	}
	for _, c := range o.Name {
		if unicode.IsControl(c) || c == '<' || c == '>' {
			return "overnight name has characters that are not allowed"
		}
	}
	return ""
}

func fmtKW(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

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
	// TZ is the zone the overnight "until" times were read in.
	TZ string `json:"tz"`
	// FromPosition is set when the plan started from the truck's position.
	FromPosition *fromPosition `json:"from_position,omitempty"`
	// FarFuture is true when the trip starts more than 30 days out: the stops,
	// chargers and times come from today's network and conditions and no
	// weather forecast was used.
	FarFuture bool `json:"far_future,omitempty"`
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
		if msg := req.validate(planNow()); msg != "" {
			badRequest(w, msg)
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
		// "until" times are read in the client's zone (an IANA name, what the
		// browser reports). A missing or unrecognised zone falls back to UTC
		// rather than failing the plan; the response says which was used.
		loc, tzUsed := time.UTC, "UTC"
		if req.TZ != "" {
			if l, err := time.LoadLocation(req.TZ); err == nil {
				loc, tzUsed = l, req.TZ
			}
		}

		vid := ""
		if req.VehicleID != nil {
			vid = *req.VehicleID
		}
		rv, msg := resolveVehicle(r.Context(), mon, pool, uid, vid, req.StartingSoC, 0)
		if msg != "" && !(req.OriginFromVehicle && socFromPosition(msg)) {
			badRequest(w, msg)
			return
		}
		origin := req.Origin.waypoint()
		var fromPos *fromPosition
		if req.OriginFromVehicle {
			pos, ok := vehiclePositionFor(r.Context(), mon, pool, uid, rv.VehicleID, time.Now())
			if !ok {
				noPosition(w)
				return
			}
			origin = tripmultiday.Waypoint{Latitude: pos.Lat, Longitude: pos.Lon, Label: "Current location"}
			// An explicit starting_soc wins over the reading.
			if req.StartingSoC == nil {
				rv.StartingSoC = pos.SoC
			}
			// Planning from where the truck is leaves now: a past start is stale.
			if startAt.Before(time.Now()) {
				startAt = time.Time{}
			}
			fromPos = pos.echo()
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
			RoutePreferences:    req.RoutePreferences.toRivian(),
			PackKWh:             pack,
			StartingSoC:         rv.StartingSoC,
			StartAt:             startAt,
			Loc:                 loc,
			Origin:              origin,
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

		// The same weather, profile and cost pass the single-day plan gets, run
		// on each day from its own starting SoC and reserve. It runs while the
		// days are chained, and the overnight and the next day start from the
		// corrected arrival when that is lower than the plan's: the chain is as
		// conservative as the arrival the user is shown.
		now := planNow()
		shift, farFuture := farFutureShift(startAt, now)
		if farFuture {
			planner = shiftedPlanner{inner: planner, shift: shift, now: now}
		}
		enr := &planEnricher{pool: pool, uid: uid, mgr: mgr, settingsStore: settingsStore, wc: wc, mc: mc, vehicleID: rv.VehicleID, packKWh: pack, noWeather: farFuture}
		enr.load(r.Context())
		type dayExtras struct {
			adj   *tripWeatherAdjustmentDTO
			costs []tripadvice.CostEstimate
		}
		extras := map[int]dayExtras{}
		mreq.AdjustArrival = func(ctx context.Context, day int, plan *rivian.TripPlan, startSoC float64, target *float64) (float64, bool) {
			adj, costs := enr.enrich(ctx, plan, startSoC, target)
			extras[day] = dayExtras{adj, costs}
			if adj == nil {
				return 0, false
			}
			return adj.FinalArrivalSoC, true
		}

		out, err := tripmultiday.Plan(r.Context(), planner, mreq)
		if err != nil {
			var re *tripmultiday.RequestError
			if errors.As(err, &re) {
				badRequest(w, re.Error())
				return
			}
			slog.WarnContext(r.Context(), "multi-day plan failed", "vehicle_id", rv.VehicleID, "days", len(days), "class", errClass(err))
			writeUpstreamError(w, err)
			return
		}

		// Over the hourly planner quota: refuse the request (429) rather than
		// return a half plan. The days already planned are cached, so asking
		// again after the window costs only the days that are left.
		if out.FailedErr != nil && writeQuotaError(w, out.FailedErr) {
			return
		}

		resp := tripPlanMultidayResponse{Total: out.Total, Simulated: out.Simulated, TZ: tzUsed, FromPosition: fromPos, FarFuture: farFuture, Days: make([]tripPlanMultidayDay, 0, len(out.Days))}
		for i, d := range out.Days {
			day := tripPlanMultidayDay{Day: d}
			if ex, ok := extras[d.Index]; ok {
				day.WeatherAdjustment, day.Costs = ex.adj, ex.costs
			} else {
				// The last day is not chained from, so it was not corrected on the way.
				target := req.TargetArrivalSocPercent
				if i < len(req.Days) && req.Days[i].TargetArrivalSocPct != nil {
					target = req.Days[i].TargetArrivalSocPct
				}
				day.WeatherAdjustment, day.Costs = enr.enrich(r.Context(), d.Plan, d.DepartureSoC, target)
			}
			resp.Days = append(resp.Days, day)
		}
		if out.FailedDay > 0 {
			resp.FailedDay, resp.Error = out.FailedDay, out.Error
			if out.FailedErr != nil {
				_, body := upstreamErrorBody(out.FailedErr)
				resp.ErrorClass, _ = body["class"].(string)
				resp.ErrorReason, _ = body["reason"].(string)
				if out.PlannerFailure {
					// Only the class is logged: no gateway text, no coordinates.
					slog.WarnContext(r.Context(), "multi-day plan: a day could not be planned",
						"vehicle_id", rv.VehicleID, "failed_day", out.FailedDay, "class", errClass(out.FailedErr))
					resp.Error = out.Error + ": " + plainPlanReason(out.FailedErr)
				}
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
				kw := o.L2KW
				if r, ok := hookupRules[o.Hookup]; ok && kw == nil {
					d := r.defaultKW
					kw = &d
				}
				di.Overnight = &tripmultiday.Overnight{L2KW: kw, Until: o.Until, MaxSoCPct: o.MaxSoCPct, Name: o.Name, Hookup: o.Hookup, L2KWGiven: o.L2KW != nil}
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

// plainPlanReason words a planner failure for the person planning: no
// internals (router, gateway or package names), just what happened or what to
// do. The full error goes to the log.
func plainPlanReason(err error) string {
	var ue *rivian.UpstreamError
	switch {
	case errors.Is(err, rivian.ErrInvalidTrip):
		// These are written for the user ("no road route between these points").
		msg := err.Error()
		if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "invalid trip") {
			msg = msg[i+2:]
		}
		return msg
	case errors.Is(err, rivian.ErrNeedsReauth):
		return "reconnect your Rivian account and try again"
	case errors.Is(err, rivian.ErrUpstreamPaused):
		return "trip planning is paused for the moment"
	case errors.As(err, &ue) && ue.Class == rivian.ClassRateLimited:
		return "the planner is busy, try again in a minute"
	case errors.As(err, &ue) && ue.Class == rivian.ClassUserAction:
		return "reconnect your Rivian account and try again"
	case errors.Is(err, rivian.ErrNoFixture):
		return "no sample plan for this trip"
	}
	return "the planner could not plan this day"
}

// validate checks the request's numbers and places before anything is planned:
// coordinates in range, percentages in range, times inside the departure
// window. A non-empty result is the 400 message, naming the day.
func (req *tripPlanMultidayRequest) validate(now time.Time) string {
	if req.StartingSoC != nil && !validPct(*req.StartingSoC) {
		return "starting_soc must be between 0 and 100"
	}
	if req.PackKWh < 0 || math.IsNaN(req.PackKWh) || req.PackKWh > 1000 {
		return "pack_kwh is out of range"
	}
	if req.TargetArrivalSocPercent != nil && !validTargetPct(*req.TargetArrivalSocPercent) {
		return "target_arrival_soc_percent must be between 0 and 100"
	}
	if req.MaxOvernightSoCPct < 0 || req.MaxOvernightSoCPct > 100 || math.IsNaN(req.MaxOvernightSoCPct) {
		return "max_overnight_soc_pct must be between 0 and 100"
	}
	if req.StartAt != "" {
		t, err := time.Parse(time.RFC3339, req.StartAt)
		if err != nil {
			return "start_at must be an RFC 3339 time"
		}
		if err := departureWindowErr(t, now); err != nil {
			return "start_at " + err.Error()
		}
	}
	pt := func(what string, p tripPoint) string {
		w := p.waypoint()
		if !validLatLon(w.Latitude, w.Longitude) {
			return what + " has an invalid coordinate"
		}
		return ""
	}
	if !req.OriginFromVehicle {
		if m := pt("origin", req.Origin); m != "" {
			return m
		}
	}
	for i, d := range req.Days {
		n := "day " + itoa(i+1)
		if m := pt(n+" end", d.End); m != "" {
			return m
		}
		for j, v := range d.Vias {
			if m := pt(n+" via "+itoa(j+1), v); m != "" {
				return m
			}
		}
		if d.TargetArrivalSocPct != nil && !validTargetPct(*d.TargetArrivalSocPct) {
			return n + ": target_arrival_soc_pct must be between 0 and 100"
		}
		if d.DepartAt != "" {
			if t, err := time.Parse(time.RFC3339, d.DepartAt); err == nil {
				if err := departureWindowErr(t, now); err != nil {
					return n + ": depart_at " + err.Error()
				}
				// Days are planned in one shifted time frame (far_future.go): a
				// day cannot leave more than 30 days after the trip starts.
				start := now
				if st, err := time.Parse(time.RFC3339, req.StartAt); err == nil && st.After(now) {
					start = st
				}
				if t.After(start.Add(farFutureAfter)) {
					return n + ": depart_at must be within 30 days of the trip's start"
				}
			}
		}
		if o := d.Overnight; o != nil {
			// 19.2 kW is the highest station rating the listing carries; the plan
			// limits the charge to the truck's AC maximum whatever is sent.
			if o.L2KW != nil && (*o.L2KW < 0 || *o.L2KW > 19.2 || math.IsNaN(*o.L2KW)) {
				return n + ": overnight l2_kw is out of range"
			}
			if m := ownStopErr(o); m != "" {
				return n + ": " + m
			}
			if o.MaxSoCPct < 0 || o.MaxSoCPct > 100 || math.IsNaN(o.MaxSoCPct) {
				return n + ": overnight max_soc_pct must be between 0 and 100"
			}
		}
	}
	if len(req.Days) == 0 {
		for i, ov := range req.Overnights {
			if !validLatLon(ov.Latitude, ov.Longitude) {
				return "overnight " + itoa(i+1) + " has an invalid coordinate"
			}
		}
		if m := pt("destination", req.Destination); m != "" && len(req.Overnights) > 0 {
			return m
		}
	}
	return ""
}

// errClass is the failure class of a planner error for logs: the upstream
// class when there is one, else "internal". Never the error text, which can
// carry the gateway's wording.
func errClass(err error) string {
	_, body := upstreamErrorBody(err)
	if c, _ := body["class"].(string); c != "" {
		return c
	}
	return "other"
}
