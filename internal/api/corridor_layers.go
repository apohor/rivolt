package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/settings"
)

const kmPerMile = 1.609344

// corridorLayersRequest is the planner map's layer query: several kinds in one pass, along the whole route, in route order.
// Page with after_km (the previous response's next_after_km) and limit.
type corridorLayersRequest struct {
	Path  [][2]float64 `json:"path"`
	Kinds []string     `json:"kinds"`
	// CorridorKm is the half-width for every kind (capped at chargers.MaxCorridorKm). Unset: each kind has its own (chargers.LayerKindKm).
	CorridorKm float64 `json:"corridor_km,omitempty"`
	MinPowerKW float64 `json:"min_power_kw,omitempty"`
	Limit      int     `json:"limit,omitempty"`
	AfterKm    float64 `json:"after_km,omitempty"`
	// Paged asks for route-ordered pages (first Limit, then next_after_km). Without it a corridor with more stations than the limit comes back
	// spread along the whole route, so a client showing one capped answer shows the entire route and not just its first stretch.
	Paged bool `json:"paged,omitempty"`
	// Preset is the planner settings' "how far from the route" choice (close, normal, wide); absent is normal. Ignored when corridor_km is set.
	Preset string `json:"preset,omitempty"`
	// StartExcludeMi drops stations within this many miles of the route's start, along the route: 0, 25 or 50 (the planner settings' choices).
	StartExcludeMi int `json:"start_exclude_mi,omitempty"`
	// MinFastKW drops fast chargers of a known power below it: 0, 50, 150 or 250.
	MinFastKW int `json:"min_fast_kw,omitempty"`
	// Networks keeps only stations of these networks (slugs of the user's price book: ea, tesla_sc, ran, evgo, shell_recharge,
	// chargepoint, ionna, flo, ev_connect, walmart). Empty: every network.
	Networks []string `json:"networks,omitempty"`
}

// layerPOI is a corridor station plus its charging network as the planner knows it: NetworkSlug joins the user's price book
// (settings charging networks: slug and preferred), RivianNetworkIDs are the ids Rivian's planner takes as network
// preferences (empty for a network Rivian does not list, or one that is not matched). NetworkName is the display name.
type layerPOI struct {
	chargers.CorridorPOI
	NetworkSlug      string   `json:"networkSlug,omitempty"`
	NetworkName      string   `json:"networkName,omitempty"`
	RivianNetworkIDs []string `json:"rivianNetworkIds,omitempty"`
}

// stationNetwork matches a station to a known network the way the planner's cost and preference code does: its name and the
// archive's network field together. ok is false for a station of no known network.
func stationNetwork(p chargers.POI) (dcfcrates.Network, bool) {
	n := dcfcrates.MatchNetwork(p.Name + " " + p.EVNetwork)
	return n, n.Slug != dcfcrates.UnmatchedNetwork.Slug
}

type corridorLayersResponse struct {
	POIs        []layerPOI `json:"pois"`
	Count       int        `json:"count"`
	Total       int        `json:"total"`
	NextAfterKm *float64   `json:"next_after_km,omitempty"`
	// AsOf is the newest export date among the stations, else the archive file's own date: when the data was built, not live. The key is always
	// present; it is "" only when neither is known, and the client then says nothing about the age.
	AsOf    string  `json:"asof"`
	RouteKm float64 `json:"route_km"`
}

// handleCorridorLayers serves POST /api/maps/corridor-layers: the charger, hotel and campground layers along a whole route.
// Archive only: nothing leaves the server, and nothing about the user is read. Stations near the route's ends are included
// (the older chargers-along trims 32 km at both ends; a layer over the whole route must not).
func handleCorridorLayers(a *chargers.Archive) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			http.Error(w, "chargers archive disabled", http.StatusNotFound)
			return
		}
		var req corridorLayersRequest
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
		kindKm, ok := chargers.CorridorPresets[req.Preset]
		if req.Preset == "" {
			kindKm, ok = chargers.CorridorPresets["normal"], true
		}
		if !ok {
			http.Error(w, "preset must be close, normal or wide", http.StatusBadRequest)
			return
		}
		if !slices.Contains(settings.StartExcludeMiles, req.StartExcludeMi) || !slices.Contains(settings.MinFastKWs, req.MinFastKW) {
			http.Error(w, "start_exclude_mi must be 0, 25 or 50 and min_fast_kw 0, 50, 150 or 250", http.StatusBadRequest)
			return
		}
		req.Path = chargers.ThinPath(req.Path, chargers.MaxPathPoints)
		if a.LoadedAt().IsZero() {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if err := a.Reload(ctx); err != nil {
				http.Error(w, "chargers archive unavailable: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		var accept func(chargers.POI) bool
		if len(req.Networks) > 0 {
			known := map[string]bool{}
			for _, n := range dcfcrates.Networks {
				known[n.Slug] = true
			}
			want := map[string]bool{}
			for _, slug := range req.Networks {
				if !known[slug] {
					http.Error(w, "unknown network "+slug, http.StatusBadRequest)
					return
				}
				want[slug] = true
			}
			accept = func(p chargers.POI) bool {
				n, ok := stationNetwork(p)
				return ok && want[n.Slug]
			}
		}
		res, err := a.QueryLayers(req.Path, req.Kinds, chargers.LayerOptions{
			QueryCorridorOptions: chargers.QueryCorridorOptions{
				CorridorKm: req.CorridorKm, MinPowerKW: req.MinPowerKW, IncludeEndpoints: true, Context: r.Context(),
			},
			KindKm: kindKm, MinAlongKm: float64(req.StartExcludeMi) * kmPerMile, MinFastKW: float64(req.MinFastKW),
			AfterKm: req.AfterKm, Limit: req.Limit, Paged: req.Paged, Accept: accept,
		})
		if errors.Is(err, chargers.ErrBadKinds) {
			http.Error(w, "kinds must be one or more of dcfc, l2, hotels, camp", http.StatusBadRequest)
			return
		}
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
		pois := make([]layerPOI, 0, len(res.POIs))
		for _, c := range res.POIs {
			lp := layerPOI{CorridorPOI: c}
			if n, ok := stationNetwork(c.POI); ok {
				lp.NetworkSlug, lp.NetworkName, lp.RivianNetworkIDs = n.Slug, n.DisplayName, n.RivianIDs
			}
			pois = append(pois, lp)
		}
		writeJSON(w, http.StatusOK, corridorLayersResponse{POIs: pois, Count: len(pois), Total: res.Total, NextAfterKm: res.NextAfterKm, AsOf: res.AsOf, RouteKm: res.RouteKm})
	}
}
