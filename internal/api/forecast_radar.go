package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/radar"
)

// ForecastTiles is the forecast radar as the planner map draws it: the hours of the newest usable cycle, and one hour as a PNG tile.
// *radar.Store satisfies it, so Deps.PlanRadar carries it when the install reads the in-cluster radar service.
type ForecastTiles interface {
	ForecastHours(ctx context.Context) (radar.ForecastHours, error)
	Tile(ctx context.Context, cycle string, fh, z, x, y int) ([]byte, error)
}

// forecastRadarMount is what api.go mounts at /api/maps/forecast-radar (Pro only; a free account gets 402 pro_required):
//
//	GET /hours                               -> {cycle, credit, bounds, hours:[{fh, valid_unix}], horizon_unix}; 503 {reason} when there is no usable cycle
//	GET /{cycle}/{fh}/{z}/{x}/{y}.png        -> a 256x256 PNG, transparent where the forecast has no value; 404 for a cycle or hour not offered
//
// The tile address names the cycle and the hour, so a tile never changes and is cached for a day. Nothing leaves the cluster: the browser never
// asks NOAA, and NOAA is not asked at all (the grids come from the radar service).
func forecastRadarMount(ls *limiterSet, ft ForecastTiles, ent *entitlements.Service) http.Handler {
	h := http.StripPrefix("/api/maps/forecast-radar", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/hours" {
			hs, err := ft.ForecastHours(r.Context())
			if err != nil {
				w.Header().Set("Retry-After", "300")
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"reason": "no_forecast"})
				return
			}
			w.Header().Set("Cache-Control", "private, max-age=60")
			writeJSON(w, http.StatusOK, hs)
			return
		}
		cycle, fh, z, x, y, ok := parseForecastTilePath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := ft.Tile(r.Context(), cycle, fh, z, x, y)
		if err != nil {
			if errors.Is(err, radar.ErrNoForecast) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "bad tile", http.StatusBadRequest)
			return
		}
		hd := w.Header()
		hd.Set("Content-Type", "image/png")
		hd.Set("Cache-Control", "private, max-age=86400, immutable")
		hd.Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(b)
	}))
	return ls.user("maps-forecast-radar", mapsRadarPerMin).Middleware(proOnlyMiddleware(ent)(h))
}

// parseForecastTilePath parses "/{cycle}/{fh}/{z}/{x}/{y}.png" strictly: a cycle is ten digits (YYYYMMDDHH), the numbers have no leading zeros or
// signs, and the tile is inside its zoom's grid and the served zoom range.
func parseForecastTilePath(p string) (cycle string, fh, z, x, y int, ok bool) {
	if !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, ".png") {
		return
	}
	parts := strings.Split(strings.TrimSuffix(p[1:], ".png"), "/")
	if len(parts) != 5 || len(parts[0]) != 10 {
		return
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return
		}
	}
	var n [4]int
	for i, s := range parts[1:] {
		if s == "" || len(s) > 6 || (len(s) > 1 && s[0] == '0') {
			return
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return
			}
		}
		n[i], _ = strconv.Atoi(s)
	}
	fh, z, x, y = n[0], n[1], n[2], n[3]
	if z < radar.ForecastMinZoom || z > radar.ForecastMaxZoom || x >= 1<<z || y >= 1<<z {
		return
	}
	return parts[0], fh, z, x, y, true
}
