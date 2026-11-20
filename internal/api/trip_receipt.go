package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/tripshare"
)

// tripReceiptKey is the design key that turns the receipt on (preview first). Off, the route does not exist.
const tripReceiptKey = "trip_receipt"

// handleTripReceipt serves POST /api/trips/saved/{id}/receipt {"sketch": bool, "metric": bool}: the data for the receipt image of a saved trip,
// which the browser draws on the device. Stateless: nothing is stored, no link is made, and no coordinate is logged. Places come back as areas
// (a reverse lookup on our own geocoder of the point rounded to the grid), figures near the ends are rounded, and the route sketch is sent only
// when asked for, cut at the start, the destination, every added stop and home. See tripshare.BuildReceipt.
func handleTripReceipt(tf *trips.Factory, sf *settings.Factory, photon *geocoding.PhotonClient) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Sketch bool `json:"sketch"`
			Metric bool `json:"metric"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		saved, err := tf.For(uid).Get(r.Context(), id)
		if errors.Is(err, trips.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(saved.Plan) == 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "this trip has no plan", "code": "unsupported"})
			return
		}
		var home *tripshare.Circle
		if sf != nil {
			if h, err := settings.GetHomeLocation(r.Context(), sf.For(uid)); err == nil && h.Set {
				home = &tripshare.Circle{Lat: h.Latitude, Lon: h.Longitude, RadiusM: rivian.HomeRadiusM}
			}
		}
		var in struct {
			DepartAt string `json:"depart_at"`
		}
		_ = json.Unmarshal(saved.Inputs, &in)
		type key struct{ lat, lon float64 }
		names := map[key]string{}
		rec, err := tripshare.BuildReceipt(r.Context(), tripshare.ReceiptInput{
			Input:  tripshare.Input{Plan: saved.Plan, DepartAt: in.DepartAt, PlannedAt: saved.UpdatedAt, Home: home, CutM: tripshare.RandomCut(randFloat())},
			Sketch: req.Sketch,
			Metric: req.Metric,
			Name: func(ctx context.Context, lat, lon float64) string {
				if n, ok := names[key{lat, lon}]; ok {
					return n
				}
				n, _ := photon.ReverseArea(ctx, lat, lon) // no name is "an area", never the exact place
				names[key{lat, lon}] = n
				return n
			},
		})
		switch {
		case errors.Is(err, tripshare.ErrTooShort):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "too short for a receipt", "code": "too_short"})
		case errors.Is(err, tripshare.ErrUnsupported), errors.Is(err, tripshare.ErrNoLegs):
			writeJSON(w, http.StatusConflict, map[string]any{"error": "this trip has no receipt yet", "code": "unsupported"})
		case err != nil:
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusOK, rec)
		}
	}
}

func designPreviewOn(d Deps, key string) bool { return slices.Contains(d.designKeys(), key) }

// designKeys is the design-key list now: the live store's when wired, else the fixed one.
func (d Deps) designKeys() []string {
	if d.DesignKeys != nil {
		return d.DesignKeys()
	}
	return d.DesignPreview
}

// requireDesignKeyMW answers the router's own 404 unless the design key is on at this moment. It runs first, so a key that is off is the same 404 as an
// unknown path (no sign-in or ownership answer differs), and a key turned on at runtime opens the route without a restart.
func requireDesignKeyMW(d Deps, key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !designPreviewOn(d, key) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
