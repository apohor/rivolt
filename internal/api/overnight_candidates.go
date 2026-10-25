package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/geocoding"
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
	// Kinds are the stays to suggest: "hotel", "campground". Omitted: hotels
	// only, as before.
	Kinds []string `json:"kinds,omitempty"`
}

// overnightResponse adds the caveat the SPA must show to the suggestion.
type overnightResponse struct {
	*overnight.Result
	// Source says what the list is: a static listing, not live availability.
	Source string `json:"source"`
	// Attribution is the credit line the page must show when any candidate
	// is a Recreation.gov campground (CC BY 4.0); empty otherwise.
	Attribution string `json:"attribution,omitempty"`
	// AttributionSourceURL and AttributionLicenseURL are the links CC BY 4.0
	// needs: the dataset and the licence.
	AttributionSourceURL  string `json:"attribution_source_url,omitempty"`
	AttributionLicenseURL string `json:"attribution_license_url,omitempty"`
	// PathSnapped is true when the request's path had straight stretches over 30 km and they were replaced by the road between their ends,
	// so the day-end point and the corridor are on the road (a planner's waypoints are far apart).
	PathSnapped bool `json:"path_snapped,omitempty"`
}

// handleOvernightCandidates serves POST /api/trips/overnight-candidates: up to
// three hotels with a listed Level 2 charger near where the day's drive budget
// runs out. It reads only the charger archive (no Rivian call) and never
// commits anything: the user picks. The list is a snapshot of a public
// listing, not availability, and says so.
func handleOvernightCandidates(a *chargers.Archive, router pathRouter, lodging lodgingSource) http.HandlerFunc {
	if a == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			jsonError(w, http.StatusNotFound, "overnight suggestions are not available here")
		}
	}
	return overnightHandlerWith(a, router, lodging, func(ctx context.Context) error {
		// Lazy load if the startup warm failed, as chargers-along does.
		if !a.LoadedAt().IsZero() {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return a.Reload(ctx)
	})
}

// overnightHandler is the handler over any archive, with no router to put a sparse path on the road; ensure loads the archive if needed.
func overnightHandler(arch overnight.Archive, ensure func(context.Context) error) http.HandlerFunc {
	return overnightHandlerWith(arch, nil, nil, ensure)
}

// lodgingSource is the self-hosted Photon's hotel and motel lookup (nil: no fallback to stays with no listed charger).
type lodgingSource interface {
	Enabled() bool
	Lodging(ctx context.Context, lat, lon, radiusKm float64, limit int) ([]geocoding.Lodging, error)
}

// lodgingTimeout bounds all the lodging lookups of one request together.
const lodgingTimeout = 8 * time.Second

// overnightHandlerWith is overnightHandler with a router (our own Valhalla; nil leaves a sparse path as it came) and a lodging source
// (our own Photon; nil leaves out the fallback to hotels with no listed charger).
func overnightHandlerWith(arch overnight.Archive, router pathRouter, lodging lodgingSource, ensure func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req overnightRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, routeBodyMax)).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		// A path of a few waypoints has chords that leave the road: put them on it before measuring where the day ends.
		var snapped bool
		req.Path, snapped = densifyPath(r.Context(), router, req.Path)
		// A long drive's polyline can have more points than a suggestion needs; thin it, as chargers-along does.
		if len(req.Path) > overnight.MaxPathPoints && len(req.Path) <= chargers.MaxRoutePoints {
			req.Path = chargers.ThinPath(req.Path, overnight.MaxPathPoints)
		}
		if err := ensure(r.Context()); err != nil {
			jsonError(w, http.StatusServiceUnavailable, "the charger listing is unavailable right now; try again shortly")
			return
		}
		oreq := overnight.Request{
			Path: req.Path, DriveSec: req.DriveSec, BudgetHours: req.BudgetHours, BudgetMiles: req.BudgetMiles,
			Kinds: req.Kinds,
		}
		if lodging != nil && lodging.Enabled() {
			lctx, cancel := context.WithTimeout(r.Context(), lodgingTimeout)
			defer cancel()
			oreq.FindLodging = func(lat, lon, radiusKm float64) ([]overnight.Lodging, error) {
				found, err := lodging.Lodging(lctx, lat, lon, radiusKm, 20)
				if err != nil {
					return nil, err
				}
				out := make([]overnight.Lodging, 0, len(found))
				for _, f := range found {
					out = append(out, overnight.Lodging{Name: f.Name, Lat: f.Lat, Lon: f.Lon, ID: f.ID, Locality: f.Locality})
				}
				return out, nil
			}
		}
		res, err := overnight.Suggest(arch, oreq)
		if err != nil {
			if errors.Is(err, overnight.ErrBadRequest) {
				jsonError(w, http.StatusBadRequest, err.Error())
				return
			}
			jsonError(w, http.StatusInternalServerError, "could not look up hotels")
			return
		}
		out := overnightResponse{Result: res, PathSnapped: snapped, Source: "NREL charging-station listing (a snapshot; availability is not checked)"}
		for _, c := range res.Candidates {
			if c.Attribution != "" {
				out.Source = "NREL charging-station listing and Recreation.gov campgrounds (a snapshot; availability is not checked)"
				out.Attribution = overnight.Attribution
				out.AttributionSourceURL = overnight.AttributionSourceURL
				out.AttributionLicenseURL = overnight.AttributionLicenseURL
				break
			}
		}
		if len(res.FallbackStays) > 0 {
			// Hotels with no listed charger come from OpenStreetMap (ODbL): credit it, with the licence and source.
			out.Source = "OpenStreetMap hotels and motels with no charger listed, paired with the nearest fast charger in the NREL listing (a snapshot; availability is not checked)"
			out.Attribution = overnight.OSMAttribution
			out.AttributionSourceURL = overnight.OSMAttributionURL
			out.AttributionLicenseURL = overnight.OSMAttributionLicense
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// jsonError is the error shape of the trip endpoints: JSON {"error": "..."}.
func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
