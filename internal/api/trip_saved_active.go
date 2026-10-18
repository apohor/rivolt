package api

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/tripmultiday"
	"github.com/apohor/rivolt/internal/trips"
)

// savedActiveResponse is GET /api/trips/saved/{id}/active: where a saved
// multi-day trip stands now, for "You're on day 2 of 3: re-plan from here".
type savedActiveResponse struct {
	Version     int    `json:"version"`
	SavedTripID string `json:"saved_trip_id"`
	// Kind is "multiday" or "single": only a multi-day trip has days to be on.
	Kind     string `json:"kind"`
	DayCount int    `json:"day_count"`
	// State is where "now" falls against the saved plan's days: "before" the
	// first departure, "active" on a day, or "after" the last day.
	State string `json:"state"`
	// CurrentDay is the day (1..N) now falls in when State is "active".
	CurrentDay *int `json:"current_day"`
	// RemainingDays counts the current day and those after it.
	RemainingDays int `json:"remaining_days"`
	// Position is the truck's latest reading, null when there is no recent one
	// (re-planning would then answer 409 no_position).
	Position *fromPosition `json:"position"`
	// Phase is "driving" on a day, or "overnight" once the day's end is
	// reached and the truck is parked there; empty when it cannot be told.
	Phase string `json:"phase,omitempty"`
	// NextDay, NextDepartAt and ProjectedSoC are set in the overnight phase:
	// the day that leaves next, when, and the charge the truck should leave
	// with after the rest of the stay.
	NextDay      *int     `json:"next_day,omitempty"`
	NextDepartAt string   `json:"next_depart_at,omitempty"`
	ProjectedSoC *float64 `json:"projected_soc,omitempty"`
	// ViasPassed are the current day's via stops (1-based) the truck has gone by.
	ViasPassed []int `json:"vias_passed,omitempty"`
}

const nearEndM = 2000.0

func distM(aLat, aLon, bLat, bLon float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat, dLon := (bLat-aLat)*rad, (bLon-aLon)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(aLat*rad)*math.Cos(bLat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// detail fills the phase, the overnight projection and the vias passed for the
// current day (index i) from the truck's position.
func (o *savedActiveResponse) detail(days []savedDayJSON, i int, now time.Time, pos *fromPosition, packKWh float64) {
	if pos == nil || days[i].End == nil {
		return
	}
	d := days[i]
	eLat, eLon := d.End.coords()
	toEnd := distM(pos.Lat, pos.Lon, eLat, eLon)
	o.Phase = "driving"
	if arrive, err := time.Parse(time.RFC3339, d.ArriveAt); err == nil && !now.Before(arrive) && toEnd <= nearEndM && i+1 < len(days) {
		o.Phase = "overnight"
		n := i + 2
		o.NextDay = &n
		if dep, err := time.Parse(time.RFC3339, days[i+1].DepartAt); err == nil {
			o.NextDepartAt = dep.UTC().Format(time.RFC3339)
			if packKWh > 0 {
				var l2 *float64
				cp := 0.0
				if d.Overnight != nil {
					l2, cp = d.Overnight.L2KW, d.Overnight.MaxSoCPc
				}
				v := tripmultiday.OvernightSoC(pos.SoC, math.Max(dep.Sub(now).Hours(), 0), l2, packKWh, cp)
				o.ProjectedSoC = &v
			}
		}
		return
	}
	// A via is passed once the truck is at least 2 km closer to the day's end than it.
	for k, v := range d.Vias {
		vLat, vLon := v.coords()
		if distM(vLat, vLon, eLat, eLon)-toEnd >= nearEndM {
			o.ViasPassed = append(o.ViasPassed, k+1)
		}
	}
}

// dayWindows returns, for each saved day, when it starts and when its window
// ends: the next day's departure, or for the last day 12 hours after it is due
// to arrive.
func dayWindows(days []savedDayJSON) (starts, ends []time.Time, ok bool) {
	for _, d := range days {
		t, err := time.Parse(time.RFC3339, d.DepartAt)
		if err != nil {
			return nil, nil, false
		}
		starts = append(starts, t.UTC())
	}
	if len(starts) == 0 {
		return nil, nil, false
	}
	for i := range starts {
		if i+1 < len(starts) {
			ends = append(ends, starts[i+1])
			continue
		}
		end := starts[i].Add(24 * time.Hour)
		if t, err := time.Parse(time.RFC3339, days[i].ArriveAt); err == nil && t.After(starts[i]) {
			end = t.UTC().Add(12 * time.Hour)
		}
		ends = append(ends, end)
	}
	return starts, ends, true
}

// activeAt works out where the saved trip stands at `now`. Pure: the plan, the
// time, and the position (nil when there is no recent reading).
func activeAt(st *trips.SavedTrip, now time.Time, pos *fromPosition, packKWh float64) savedActiveResponse {
	out := savedActiveResponse{Version: 1, SavedTripID: st.ID.String(), Kind: "single", State: "none", Position: pos}
	days, multi := savedMultiday(st.Plan)
	if !multi {
		return out
	}
	out.Kind, out.DayCount = "multiday", len(days)
	starts, ends, ok := dayWindows(days)
	if !ok {
		return out
	}
	switch {
	case now.Before(starts[0]):
		out.State, out.RemainingDays = "before", len(days)
	case !now.Before(ends[len(ends)-1]):
		out.State = "after"
	default:
		out.State = "active"
		for i := range days {
			if !now.Before(starts[i]) && now.Before(ends[i]) {
				n := i + 1
				out.CurrentDay, out.RemainingDays = &n, len(days)-i
				out.detail(days, i, now, pos, packKWh)
				break
			}
		}
	}
	return out
}

// handleSavedTripActive answers where the saved trip stands: which day is
// under way and the truck's position. It reads the saved plan's day times and
// the truck's latest reading; it plans nothing and spends nothing upstream.
func handleSavedTripActive(tf *trips.Factory, mon func(uuid.UUID) *rivian.StateMonitor, pool *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		st, err := tf.For(uid).Get(r.Context(), id)
		if errors.Is(err, trips.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			planError(w, http.StatusInternalServerError, "trip lookup failed")
			return
		}
		now := time.Now().UTC()
		var pos *fromPosition
		// Only the vehicle id is wanted here; whether a starting charge is known
		// does not matter, so the resolver's message is not an error.
		rv, msg := resolveVehicle(r.Context(), mon(uid), pool, uid, r.URL.Query().Get("vehicle_id"), nil, 0)
		if msg == "unknown vehicle" {
			badRequest(w, msg)
			return
		}
		var pack float64
		if rv.VehicleID != "" {
			pack = vehiclePackKWh(r.Context(), pool, uid, rv.VehicleID)
			if p, ok := vehiclePositionFor(r.Context(), mon(uid), pool, uid, rv.VehicleID, now); ok {
				pos = p.echo()
			}
		}
		writeJSON(w, http.StatusOK, activeAt(st, now, pos, pack))
	}
}
