package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
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
func activeAt(st *trips.SavedTrip, now time.Time, pos *fromPosition) savedActiveResponse {
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
		rv, _ := resolveVehicle(r.Context(), mon(uid), pool, uid, "", nil, 0)
		if rv.VehicleID != "" {
			if p, ok := vehiclePositionFor(r.Context(), mon(uid), pool, uid, rv.VehicleID, now); ok {
				pos = p.echo()
			}
		}
		writeJSON(w, http.StatusOK, activeAt(st, now, pos))
	}
}
