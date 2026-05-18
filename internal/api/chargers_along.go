package api

import (
	"context"
	"encoding/json"
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Path) < 2 {
			http.Error(w, "path requires >= 2 points", http.StatusBadRequest)
			return
		}
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
		})
		if err != nil {
			http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, chargersAlongResponse{Chargers: out, Count: len(out)})
	}
}
