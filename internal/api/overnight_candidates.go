package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/overnight"
)

// overnightRequest is POST /api/trips/overnight-candidates: one planned day's
// route and how much driving the day should hold.
type overnightRequest struct {
	// Path is the day's route as [lat, lon] pairs, start to end.
	Path [][2]float64 `json:"path"`
	// DriveSec is the route's driving time in seconds (so the budget can be
	// turned into a distance). Omitted: 90 km/h is assumed.
	DriveSec float64 `json:"drive_sec,omitempty"`
	// BudgetHours is the most driving in a day (default 8). BudgetMiles, when
	// given, is used instead.
	BudgetHours float64 `json:"budget_hours,omitempty"`
	BudgetMiles float64 `json:"budget_miles,omitempty"`
}

// overnightResponse adds the caveat the SPA must show to the suggestion.
type overnightResponse struct {
	*overnight.Result
	// Source says what the list is: a static listing, not live availability.
	Source string `json:"source"`
}

// handleOvernightCandidates serves POST /api/trips/overnight-candidates: up to
// three hotels with a listed Level 2 charger near where the day's drive budget
// runs out. It reads only the charger archive (no Rivian call) and never
// commits anything: the user picks. The list is a snapshot of a public
// listing, not availability, and says so.
func handleOvernightCandidates(a *chargers.Archive) http.HandlerFunc {
	if a == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			jsonError(w, http.StatusNotFound, "overnight suggestions are not available here")
		}
	}
	return overnightHandler(a, func(ctx context.Context) error {
		// Lazy load if the startup warm failed, as chargers-along does.
		if !a.LoadedAt().IsZero() {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return a.Reload(ctx)
	})
}

// overnightHandler is the handler over any archive; ensure loads it if needed.
func overnightHandler(arch overnight.Archive, ensure func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req overnightRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := ensure(r.Context()); err != nil {
			jsonError(w, http.StatusServiceUnavailable, "the charger listing is unavailable right now; try again shortly")
			return
		}
		res, err := overnight.Suggest(arch, overnight.Request{
			Path: req.Path, DriveSec: req.DriveSec, BudgetHours: req.BudgetHours, BudgetMiles: req.BudgetMiles,
		})
		if err != nil {
			if errors.Is(err, overnight.ErrBadRequest) {
				jsonError(w, http.StatusBadRequest, err.Error())
				return
			}
			jsonError(w, http.StatusInternalServerError, "could not look up hotels")
			return
		}
		writeJSON(w, http.StatusOK, overnightResponse{Result: res, Source: "NREL charging-station listing (a snapshot; availability is not checked)"})
	}
}

// jsonError is the error shape of the trip endpoints: JSON {"error": "..."}.
func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
