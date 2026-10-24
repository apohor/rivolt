package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
)

// chargersAlongRequest is the SPA-facing input. Path is an array of
// [lat, lon] pairs in WGS84 — the route polyline. Filter matches the
// existing SPA ChargerFilter strings ("dcfc" / "l2" / "hotels" / "all").
type chargersAlongRequest struct {
	Path       [][2]float64 `json:"path"`
	Filter     string       `json:"filter,omitempty"`
	CorridorKm float64      `json:"corridor_km,omitempty"`
	MinPowerKW float64      `json:"min_power_kw,omitempty"`
}

type chargersAlongResponse struct {
	Chargers []chargers.POI `json:"chargers"`
	Count    int            `json:"count"`
}

// handleChargersAlong serves POST /api/maps/chargers-along. Lazy-loads
// the archive on first request when the startup warm failed; honours
// the 30s timeout middleware on the route group.
func handleChargersAlong(a *chargers.Archive) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			http.Error(w, "chargers archive disabled", http.StatusNotFound)
			return
		}
		var req chargersAlongRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, routeBodyMax)).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Path) < 2 {
			http.Error(w, "path requires >= 2 points", http.StatusBadRequest)
			return
		}
		if len(req.Path) > chargers.MaxRoutePoints {
			http.Error(w, "path has too many points", http.StatusBadRequest)
			return
		}
		// A long drive's polyline has tens of thousands of points; the query takes a few thousand, evenly spaced
		// along the route, which follows the road to within a few hundred metres.
		req.Path = chargers.ThinPath(req.Path, chargers.MaxPathPoints)
		// Lazy load if the startup warm failed.
		if a.LoadedAt().IsZero() {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if err := a.Reload(ctx); err != nil {
				http.Error(w, "chargers archive unavailable: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		out, err := a.QueryCorridor(req.Path, chargers.Filter(req.Filter), chargers.QueryCorridorOptions{
			CorridorKm: req.CorridorKm,
			MinPowerKW: req.MinPowerKW,
			Context:    r.Context(),
		})
		if errors.Is(err, chargers.ErrQueryTooLarge) || errors.Is(err, chargers.ErrBadPath) {
			http.Error(w, "path is too large or not valid", http.StatusBadRequest)
			return
		}
		if r.Context().Err() != nil {
			return // the client went away
		}
		if err != nil {
			http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, chargersAlongResponse{Chargers: out, Count: len(out)})
	}
}
