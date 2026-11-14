package api

import (
	"errors"
	"hash/fnv"
	"math"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/histwin"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripshare"
)

// drivePostcard is GET /api/drives/{id}/postcard: what the Activity "Share trip" card draws a finished drive from. The route is cut on
// the server with the trip-share code (both ends and the saved home), so the client never holds the untrimmed line and trims nothing.
// It carries no vehicle id, no cost, no energy and no time of day: the date, the distance and the trimmed route.
type drivePostcard struct {
	Version    int                `json:"version"`
	Date       string             `json:"date"` // "2026-03-05", UTC
	DistanceMi float64            `json:"distance_mi"`
	Start      tripshare.Area     `json:"start"`
	End        tripshare.Area     `json:"end"`
	Route      tripshare.RouteOut `json:"route"`
}

// postcardCut is the cut radius for one drive: drawn once from the drive, not per request, so asking again never gives a shorter cut.
func postcardCut(userKey, driveID string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userKey + "/" + driveID))
	return tripshare.RandomCut(float64(h.Sum64()>>11) / (1 << 53))
}

func handleDrivePostcard(store *drives.Store, settingsStore *settings.Store, userKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		d, err := store.Get(r.Context(), id)
		if err != nil {
			http.Error(w, "could not read the drive", http.StatusInternalServerError)
			return
		}
		if d == nil {
			http.NotFound(w, r)
			return
		}
		if !histwin.Allows(r.Context(), d.StartedAt) {
			writeOutsideFreeWindow(w, r)
			return
		}
		var home *tripshare.Circle
		if h, err := settings.GetHomeLocation(r.Context(), settingsStore); err == nil && h.Set {
			home = &tripshare.Circle{Lat: h.Latitude, Lon: h.Longitude, RadiusM: rivian.HomeRadiusM}
		}
		trim, err := tripshare.TrimDrive(drives.DecodeRoute(d.RoutePolyline), home, postcardCut(userKey, d.ID))
		switch {
		case errors.Is(err, tripshare.ErrNoRoute):
			writeJSON(w, http.StatusConflict, map[string]any{"error": "this drive has no route to share", "code": "no_route"})
			return
		case errors.Is(err, tripshare.ErrTooShort):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "this drive is too short to share without showing where it starts", "code": "too_short"})
			return
		case err != nil:
			http.Error(w, "could not build the postcard", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, drivePostcard{
			Version:    1,
			Date:       d.StartedAt.UTC().Format("2006-01-02"),
			DistanceMi: math.Round(d.DistanceMi*10) / 10,
			Start:      trim.Start,
			End:        trim.End,
			Route:      tripshare.RouteOut{Segments: trim.Segments},
		})
	}
}
