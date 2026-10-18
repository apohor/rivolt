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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripmultiday"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/weather"
)

// Re-plan from here: a saved multi-day trip re-planned from the truck's
// position, SoC and time. The days already driven are kept as saved; the
// current day's remaining stops and every later day are planned again with
// their original inputs, through the same leg cache and quota as
// plan-multiday. Nothing is written: the client saves the result with the
// saved-trip PUT.

const (
	replanNearKm      = 2.0              // "at" a day's end, and the margin a via must be passed by
	replanPositionAge = 15 * time.Minute // older fixes are not "here"
)

// replanBody is the request. Every field is optional: position and SoC default
// to the vehicle's latest state, at to the server clock.
type replanBody struct {
	Position *struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"position"`
	SoC      *float64 `json:"soc"`
	At       string   `json:"at"`
	TZ       string   `json:"tz"`
	FromDay  int      `json:"from_day"`
	ViasDone []int    `json:"vias_done"`
}

// replanSnapshot is a saved multi-day plan: the request that produced it (when
// the client stored one) and the days it returned.
type replanSnapshot struct {
	Kind    string                   `json:"kind"`
	Request *tripPlanMultidayRequest `json:"request"`
	Days    []json.RawMessage        `json:"days"`
}

// replanSavedDay is the part of a saved day progress detection reads.
type replanSavedDay struct {
	DepartAt time.Time             `json:"depart_at"`
	ArriveAt time.Time             `json:"arrive_at"`
	End      tripmultiday.Waypoint `json:"end"`
	Plan     *struct {
		Routes []struct {
			TotalDriveDistanceMeters float64 `json:"TotalDriveDistanceMeters"`
			TotalDriveDurationSec    int     `json:"TotalDriveDurationSec"`
			TotalChargingDurationSec int     `json:"TotalChargingDurationSec"`
		} `json:"Routes"`
	} `json:"plan"`
}

// replanForm is the planner form a trip was saved with, for saves that predate
// the stored request (see reconstructRequest).
type replanForm struct {
	Origin                  *formPoint  `json:"origin"`
	Destination             *formPoint  `json:"destination"`
	ExtraStops              []formPoint `json:"extra_stops"`
	TargetSoC               string      `json:"target_soc"`
	DriveMode               string      `json:"drive_mode"`
	HasAdapter              *bool       `json:"has_adapter"`
	OvernightFlags          []bool      `json:"overnight_flags"`
	MaxOvernightSoCPct      float64     `json:"max_overnight_soc_pct"`
	OvernightDepartureLocal string      `json:"overnight_departure_local"`
	OvernightL2KW           *float64    `json:"overnight_l2_kw"`
}

type formPoint struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Label string  `json:"label"`
}

func (p formPoint) point() tripPoint { return tripPoint{Lat: p.Lat, Lon: p.Lon, Label: p.Label} }

// reconstructRequest rebuilds the plan-multiday request from the saved form,
// the way the planner page builds it: stops up to each overnight are that
// day's vias, the overnight ends the day, and the last day ends at the
// destination.
func reconstructRequest(f replanForm) (tripPlanMultidayRequest, bool) {
	if f.Origin == nil || f.Destination == nil {
		return tripPlanMultidayRequest{}, false
	}
	l2 := tripmultiday.DefaultL2KW // older saves have no charger field
	if f.OvernightL2KW != nil {
		l2 = *f.OvernightL2KW
	}
	var target *float64
	if v, err := strconv.ParseFloat(strings.TrimSpace(f.TargetSoC), 64); err == nil {
		target = &v
	}
	req := tripPlanMultidayRequest{Origin: f.Origin.point(), DriveMode: f.DriveMode, HasAdapter: f.HasAdapter}
	var vias []tripPoint
	for i, s := range f.ExtraStops {
		if i < len(f.OvernightFlags) && f.OvernightFlags[i] {
			kw := l2
			req.Days = append(req.Days, tripPlanDayRequest{Vias: vias, End: s.point(), TargetArrivalSocPct: target,
				Overnight: &tripPlanOvernight{L2KW: &kw, Until: f.OvernightDepartureLocal, MaxSoCPct: f.MaxOvernightSoCPct, Name: s.Label}})
			vias = nil
			continue
		}
		vias = append(vias, s.point())
	}
	req.Days = append(req.Days, tripPlanDayRequest{Vias: vias, End: f.Destination.point(), TargetArrivalSocPct: target})
	return req, true
}

// replanState is where the truck is, as the re-plan sees it.
type replanState struct {
	Lat, Lon float64
	SoC      float64
	At       time.Time
	FromDay  int   // 0: detect
	ViasDone []int // nil: detect
}

// replanPlan is the re-plan to run: the remaining trip as a plan-multiday
// request starting at the truck, and how it was derived.
type replanPlan struct {
	FromDay     int
	Phase       string // not_started | driving | overnight
	Req         tripPlanMultidayRequest
	ViasDropped []int
	Overnight   *tripmultiday.OvernightResult // the rest of the stay, overnight phase only
}

type replanError struct {
	status int
	class  string
	msg    string
}

func (e *replanError) Error() string { return e.msg }

// buildReplan works out where the truck is in the trip and returns the
// remaining trip as a request. orig is the trip's request, saved its days as
// planned. It is pure: same inputs, same answer.
func buildReplan(orig tripPlanMultidayRequest, saved []replanSavedDay, st replanState, packKWh float64) (replanPlan, *replanError) {
	n := len(orig.Days)
	if n == 0 {
		return replanPlan{}, &replanError{http.StatusBadRequest, "invalid", "the saved trip has no days"}
	}
	pos := tripPoint{Lat: st.Lat, Lon: st.Lon, Label: "Current position"}
	out := replanPlan{Phase: "driving"}
	k := st.FromDay
	if k != 0 && (k < 1 || k > n) {
		return replanPlan{}, &replanError{http.StatusBadRequest, "invalid", "from_day must be between 1 and " + strconv.Itoa(n)}
	}
	if k == 0 {
		for i, d := range saved {
			if i < n && !d.DepartAt.IsZero() && !st.At.Before(d.DepartAt) {
				k = i + 1
			}
		}
		if k == 0 {
			k, out.Phase = 1, "not_started"
		}
	}
	startAt, startSoC := st.At, st.SoC

	// Parked at the end of day k after arriving: the trip goes on with the next
	// day, from its planned departure, with what the rest of the night adds.
	if st.FromDay == 0 && out.Phase == "driving" && k <= len(saved) {
		d := saved[k-1]
		if !d.ArriveAt.IsZero() && !st.At.Before(d.ArriveAt) && distKm(st.Lat, st.Lon, d.End.Latitude, d.End.Longitude) <= replanNearKm {
			if k == n {
				return replanPlan{}, &replanError{http.StatusConflict, "trip_complete", "the trip is complete: the truck is at its destination"}
			}
			depart := st.At
			if k < len(saved) && saved[k].DepartAt.After(st.At) {
				depart = saved[k].DepartAt
			}
			ov := tripmultiday.Overnight{}
			if o := orig.Days[k-1].Overnight; o != nil {
				ov = tripmultiday.Overnight{L2KW: o.L2KW, Until: o.Until, MaxSoCPct: o.MaxSoCPct, Name: o.Name}
			}
			capPct := ov.MaxSoCPct
			if capPct <= 0 {
				capPct = orig.MaxOvernightSoCPct
			}
			res := tripmultiday.OvernightCharge(st.SoC, depart.Sub(st.At).Hours(), ov, packKWh, 0, capPct)
			out.Phase, out.Overnight = "overnight", &res
			startAt, startSoC = depart, res.PostChargeSoCPct
			k++
		}
	}
	out.FromDay = k

	days := make([]tripPlanDayRequest, 0, n-k+1)
	first := orig.Days[k-1]
	first.DepartAt = "" // the re-plan's start time governs the first day
	if out.Phase == "driving" {
		done, err := viasDone(first, st)
		if err != nil {
			return replanPlan{}, err
		}
		var keep []tripPoint
		for i, v := range first.Vias {
			if done[i] {
				out.ViasDropped = append(out.ViasDropped, i)
				continue
			}
			keep = append(keep, v)
		}
		first.Vias = keep
	}
	days = append(days, first)
	days = append(days, orig.Days[k:]...)

	req := orig
	req.VehicleID, req.StartingSoC = nil, &startSoC
	req.Origin, req.StartAt, req.Days = pos, startAt.Format(time.RFC3339), days
	req.Destination, req.Overnights = tripPoint{}, nil
	out.Req = req
	return out, nil
}

// viasDone marks the current day's vias already visited: the ones listed in
// st.ViasDone, or, by default, those the truck is closer to the day's end than
// by at least replanNearKm (straight line).
func viasDone(day tripPlanDayRequest, st replanState) (map[int]bool, *replanError) {
	done := map[int]bool{}
	if st.ViasDone != nil {
		for _, i := range st.ViasDone {
			if i < 0 || i >= len(day.Vias) {
				return nil, &replanError{http.StatusBadRequest, "invalid", "vias_done: no via " + strconv.Itoa(i) + " on this day"}
			}
			done[i] = true
		}
		return done, nil
	}
	end := day.End.waypoint()
	here := distKm(st.Lat, st.Lon, end.Latitude, end.Longitude)
	for i, v := range day.Vias {
		wp := v.waypoint()
		if here+replanNearKm < distKm(wp.Latitude, wp.Longitude, end.Latitude, end.Longitude) {
			done[i] = true
		}
	}
	return done, nil
}

// distKm is the great-circle distance in km.
func distKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// replanMeta is the response's account of the re-plan.
type replanMeta struct {
	FromDay     int                           `json:"from_day"`
	At          time.Time                     `json:"at"`
	Position    map[string]float64            `json:"position"`
	SoC         float64                       `json:"soc"`
	Phase       string                        `json:"phase"`
	ViasDropped []int                         `json:"vias_dropped"`
	Overnight   *tripmultiday.OvernightResult `json:"overnight,omitempty"`
	Basis       string                        `json:"basis"` // request | reconstructed
}

type replanResponse struct {
	Days        []json.RawMessage       `json:"days"`
	Total       tripmultiday.Totals     `json:"total"`
	TotalTrip   tripmultiday.Totals     `json:"total_trip"`
	Simulated   bool                    `json:"simulated,omitempty"`
	TZ          string                  `json:"tz"`
	FailedDay   int                     `json:"failed_day,omitempty"`
	Error       string                  `json:"error,omitempty"`
	ErrorClass  string                  `json:"error_class,omitempty"`
	ErrorReason string                  `json:"error_reason,omitempty"`
	Replan      replanMeta              `json:"replan"`
	Request     tripPlanMultidayRequest `json:"request"`
}

// withStatus returns raw (a JSON object) with "status" set, and "index" when
// index > 0.
func withStatus(raw json.RawMessage, status string, index int) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["status"], _ = json.Marshal(status)
	if index > 0 {
		m["index"], _ = json.Marshal(index)
	}
	return json.Marshal(m)
}

func handleTripReplan(c rivian.Client, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID, store *trips.Store, mgr *settings.Manager, settingsStore *settings.Store, wc *weather.Client, mc *weather.MemCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planner, ok := tripPlannerOf(c)
		if !ok {
			planError(w, http.StatusNotFound, "live rivian client required")
			return
		}
		if store == nil {
			planError(w, http.StatusUnauthorized, "no user")
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			badRequest(w, "invalid id")
			return
		}
		var body replanBody
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				badRequest(w, "invalid body: "+err.Error())
				return
			}
		}
		saved, err := store.Get(r.Context(), id)
		if errors.Is(err, trips.ErrNotFound) {
			planError(w, http.StatusNotFound, "saved trip not found")
			return
		}
		if err != nil {
			planError(w, http.StatusInternalServerError, "could not read the saved trip")
			return
		}
		var snap replanSnapshot
		if len(saved.Plan) == 0 || json.Unmarshal(saved.Plan, &snap) != nil || snap.Kind != "multiday" || len(snap.Days) == 0 {
			replanRefuse(w, &replanError{http.StatusConflict, "not_multiday", "this saved trip has no multi-day plan to re-plan"})
			return
		}
		basis := "request"
		orig := snap.Request
		if orig == nil {
			var f replanForm
			rec, ok := tripPlanMultidayRequest{}, false
			if json.Unmarshal(saved.Inputs, &f) == nil {
				rec, ok = reconstructRequest(f)
			}
			if !ok {
				replanRefuse(w, &replanError{http.StatusConflict, "not_multiday", "this saved trip's inputs can't be re-planned"})
				return
			}
			orig, basis = &rec, "reconstructed"
			if len(orig.Days) != len(snap.Days) {
				replanRefuse(w, &replanError{http.StatusConflict, "not_multiday", "this saved trip's inputs don't match its days; plan it again"})
				return
			}
		}
		days := make([]replanSavedDay, len(snap.Days))
		for i, raw := range snap.Days {
			if json.Unmarshal(raw, &days[i]) != nil {
				replanRefuse(w, &replanError{http.StatusConflict, "not_multiday", "day " + strconv.Itoa(i+1) + " of the saved plan is not readable"})
				return
			}
		}

		st := replanState{At: time.Now(), FromDay: body.FromDay, ViasDone: body.ViasDone}
		if body.At != "" {
			if st.At, err = time.Parse(time.RFC3339, body.At); err != nil {
				badRequest(w, "at must be an RFC 3339 time")
				return
			}
		}
		vid := ""
		if orig.VehicleID != nil {
			vid = *orig.VehicleID
		}
		rv, msg := resolveVehicle(r.Context(), mon, pool, uid, vid, body.SoC, 0)
		if msg != "" {
			badRequest(w, msg)
			return
		}
		st.SoC = rv.StartingSoC
		if body.Position != nil {
			st.Lat, st.Lon = body.Position.Lat, body.Position.Lon
		} else if lat, lon, ok := latestPosition(r.Context(), mon, pool, uid, rv.VehicleID, time.Now()); ok {
			st.Lat, st.Lon = lat, lon
		} else {
			replanRefuse(w, &replanError{http.StatusConflict, "position_unknown", "the truck's position is not known (no fix in the last 15 minutes); pass a position"})
			return
		}
		if st.Lat < -90 || st.Lat > 90 || st.Lon < -180 || st.Lon > 180 {
			badRequest(w, "position is not a valid coordinate")
			return
		}
		pack := orig.PackKWh
		if pack <= 0 {
			pack = vehiclePackKWh(r.Context(), pool, uid, rv.VehicleID)
		}
		if pack <= 0 {
			badRequest(w, "pack_kwh required (this vehicle's pack size is not known)")
			return
		}

		plan, rerr := buildReplan(*orig, days, st, pack)
		if rerr != nil {
			replanRefuse(w, rerr)
			return
		}
		req := plan.Req
		req.VehicleID, req.PackKWh = &rv.VehicleID, pack
		if body.TZ != "" {
			req.TZ = body.TZ
		}
		md := multidayDeps{mon: mon, pool: pool, uid: uid, mgr: mgr, settingsStore: settingsStore, wc: wc, mc: mc}
		resp, ok := planMultidayCore(r.Context(), w, planner, md, req)
		if !ok {
			return
		}

		out := replanResponse{Total: resp.Total, Simulated: resp.Simulated, TZ: resp.TZ,
			Error: resp.Error, ErrorClass: resp.ErrorClass, ErrorReason: resp.ErrorReason, Request: *orig,
			Replan: replanMeta{FromDay: plan.FromDay, At: st.At, Position: map[string]float64{"lat": st.Lat, "lon": st.Lon},
				SoC: st.SoC, Phase: plan.Phase, ViasDropped: plan.ViasDropped, Overnight: plan.Overnight, Basis: basis}}
		if out.Replan.ViasDropped == nil {
			out.Replan.ViasDropped = []int{}
		}
		if resp.FailedDay > 0 {
			out.FailedDay = resp.FailedDay + plan.FromDay - 1
		}
		for i := 0; i < plan.FromDay-1 && i < len(snap.Days); i++ {
			raw, err := withStatus(snap.Days[i], "done", 0)
			if err != nil {
				planError(w, http.StatusInternalServerError, "could not read the saved plan")
				return
			}
			out.Days = append(out.Days, raw)
			if p := days[i].Plan; p != nil && len(p.Routes) > 0 {
				out.TotalTrip.DistanceMeters += p.Routes[0].TotalDriveDistanceMeters
				out.TotalTrip.DriveDurationSec += p.Routes[0].TotalDriveDurationSec
				out.TotalTrip.ChargingDurationSec += p.Routes[0].TotalChargingDurationSec
			}
		}
		for _, d := range resp.Days {
			b, err := json.Marshal(d)
			if err == nil {
				b, err = withStatus(b, "replanned", d.Index+plan.FromDay-1)
			}
			if err != nil {
				planError(w, http.StatusInternalServerError, "could not encode the plan")
				return
			}
			out.Days = append(out.Days, b)
		}
		out.TotalTrip.DistanceMeters += resp.Total.DistanceMeters
		out.TotalTrip.DriveDurationSec += resp.Total.DriveDurationSec
		out.TotalTrip.ChargingDurationSec += resp.Total.ChargingDurationSec
		if resp.FailedDay > 0 {
			slog.WarnContext(r.Context(), "re-plan: a day could not be planned", "saved_trip", id, "failed_day", out.FailedDay, "class", out.ErrorClass)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func replanRefuse(w http.ResponseWriter, e *replanError) {
	writeJSON(w, e.status, map[string]any{"error": e.msg, "class": e.class})
}

// latestPosition is the vehicle's last GPS fix, if it is recent enough to be
// "here": the monitor's state first, then the newest vehicle_state row.
func latestPosition(ctx context.Context, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID, vehicleID string, now time.Time) (float64, float64, bool) {
	fresh := func(at time.Time) bool { return !at.IsZero() && now.Sub(at) <= replanPositionAge }
	if mon != nil && vehicleID != "" {
		if st, _ := mon.Latest(vehicleID); st != nil && (st.Latitude != 0 || st.Longitude != 0) {
			at := st.LocationFixAt
			if at.IsZero() {
				at = st.At
			}
			if fresh(at) {
				return st.Latitude, st.Longitude, true
			}
		}
	}
	if pool == nil || vehicleID == "" {
		return 0, 0, false
	}
	var lat, lon sql.NullFloat64
	var at time.Time
	err := pool.QueryRowContext(ctx, `
		SELECT lat, lon, at
		  FROM vehicle_state
		 WHERE user_id = $1
		   AND vehicle_id = (SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2 LIMIT 1)
		   AND lat IS NOT NULL AND lon IS NOT NULL
		 ORDER BY at DESC
		 LIMIT 1`, uid, vehicleID).Scan(&lat, &lon, &at)
	if err != nil || !fresh(at) {
		return 0, 0, false
	}
	return lat.Float64, lon.Float64, true
}
