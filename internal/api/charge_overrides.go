package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// chargeOverrider is the slice of *charges.Store the override handlers
// use, so they can be tested without Postgres.
type chargeOverrider interface {
	SetPlaceOverride(ctx context.Context, externalID, place string, homeSet bool, homeLat, homeLon, radiusM float64) error
	SetLocationOverride(ctx context.Context, externalID string, recorded bool) error
}

// handleChargePlacePut serves PUT /api/charges/{id}/place
// {"place": "home"|"away"|null}: the user's own home/away for one charge
// (#906). A set place is "set by you": RestampPlace, its undo and every
// later re-import leave it alone. null hands the charge back to the home
// location. View-as can't write; the audit line names the field, never a
// place or a position.
func handleChargePlacePut(store chargeOverrider, home func(context.Context) (settings.HomeLocation, error)) http.HandlerFunc {
	type body struct {
		Place *string `json:"place"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "charges disabled"})
			return
		}
		if auth.IsImpersonating(r.Context()) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "not available while viewing as another user"})
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id"})
			return
		}
		var b body
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
			return
		}
		place := ""
		if b.Place != nil {
			place = *b.Place
			if place != "home" && place != "away" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "place must be home, away or null"})
				return
			}
		}
		var h settings.HomeLocation
		if place == "" && home != nil {
			var err error
			if h, err = home(r.Context()); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		}
		err := store.SetPlaceOverride(r.Context(), id, place, h.Set, h.Latitude, h.Longitude, rivian.HomeRadiusM)
		if errors.Is(err, charges.ErrNoSuchCharge) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		action := "set"
		if place == "" {
			action = "cleared"
		}
		slog.InfoContext(r.Context(), "charge override saved", "charge", id, "fields", []string{"place_override"}, "action", action)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleChargeLocationPut serves PUT /api/charges/{id}/location
// {"recorded": bool}: true pins the charge where the truck was (its
// stored position) instead of the nearest public charger (#906); false
// goes back to the usual snap. Same rules as the place override.
func handleChargeLocationPut(store chargeOverrider) http.HandlerFunc {
	type body struct {
		Recorded *bool `json:"recorded"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "charges disabled"})
			return
		}
		if auth.IsImpersonating(r.Context()) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "not available while viewing as another user"})
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id"})
			return
		}
		var b body
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil || b.Recorded == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "recorded must be true or false"})
			return
		}
		err := store.SetLocationOverride(r.Context(), id, *b.Recorded)
		if errors.Is(err, charges.ErrNoSuchCharge) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		action := "set"
		if !*b.Recorded {
			action = "cleared"
		}
		slog.InfoContext(r.Context(), "charge override saved", "charge", id, "fields", []string{"location_override"}, "action", action)
		w.WriteHeader(http.StatusNoContent)
	}
}

// overriderFor returns the user's charges store as a chargeOverrider,
// nil (not a typed nil) when charges are disabled.
func overriderFor(d Deps, uid uuid.UUID) chargeOverrider {
	if d.Charges == nil {
		return nil
	}
	if cs := d.Charges.For(uid); cs != nil {
		return cs
	}
	return nil
}
