package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
)

// handleGeocode resolves a free-text query to a list of locations.
// When a self-hosted Photon is wired (RIVOLT_PHOTON_BASE_URL),
// queries hit Photon first for street-level resolution; on empty
// results or transient failure we fall back to Open-Meteo's city-
// level search. With Photon unwired the handler is just Open-Meteo.
func handleGeocode(photon *geocoding.PhotonClient) http.HandlerFunc {
	gc := geocoding.NewClient()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if strings.TrimSpace(q) == "" {
			writeJSON(w, http.StatusOK, []geocoding.Result{})
			return
		}
		count := 5
		if c := r.URL.Query().Get("count"); c != "" {
			if n, err := strconv.Atoi(c); err == nil && n > 0 {
				count = n
			}
		}
		var results []geocoding.Result
		if photon.Enabled() {
			r1, err := photon.Search(r.Context(), q, count)
			if err == nil && len(r1) > 0 {
				results = r1
			}
		}
		if len(results) == 0 {
			r2, err := gc.Search(r.Context(), q, count)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
				return
			}
			results = r2
		}
		if results == nil {
			results = []geocoding.Result{}
		}
		writeJSON(w, http.StatusOK, results)
	}
}

// handleHomeLocationGet returns the user's saved home location, or
// {"set": false} when none is configured. Used by the trip planner
// to decide whether to render the "Home" preset.
func handleHomeLocationGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := settings.GetHomeLocation(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, h)
	}
}

// handleHomeLocationPut writes the user's home location. Body shape
// matches HomeLocation; passing Set=false clears the saved value.
func handleHomeLocationPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var h settings.HomeLocation
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetHomeLocation(r.Context(), store, h); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Echo back what's now stored (post-validation) so the SPA
		// can update its in-memory copy without a second GET.
		out, _ := settings.GetHomeLocation(r.Context(), store)
		writeJSON(w, http.StatusOK, out)
	}
}

// handlePlannerPrefsGet returns the user's saved trip-planner
// defaults (drive mode + Tesla adapter). Used by the SPA to
// pre-fill the per-trip form.
func handlePlannerPrefsGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := settings.GetPlannerPrefs(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// handlePlannerPrefsPut persists planner defaults.
func handlePlannerPrefsPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p settings.PlannerPrefs
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetPlannerPrefs(r.Context(), store, p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := settings.GetPlannerPrefs(r.Context(), store)
		writeJSON(w, http.StatusOK, out)
	}
}

// handlePlannerFavoritesGet returns the user's list of favorite
// planner destinations. Empty array is a normal response — the
// SPA renders it the same way it renders a populated list.
func handlePlannerFavoritesGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := settings.GetPlannerFavorites(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []settings.PlannerFavorite{}
		}
		writeJSON(w, http.StatusOK, list)
	}
}

// handlePlannerFavoritesPut overwrites the favorites list wholesale.
// The SPA always sends the full array (no partial-update PATCH)
// because the storage is a single JSON blob — partial semantics
// would require split-merge logic for little gain at this scale.
func handlePlannerFavoritesPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []settings.PlannerFavorite
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetPlannerFavorites(r.Context(), store, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := settings.GetPlannerFavorites(r.Context(), store)
		if out == nil {
			out = []settings.PlannerFavorite{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// savedTripBody is the wire shape for create/update. Name is the only
// required field; plan/advice are optional snapshots from a successful
// /api/trips/plan + /api/trips/plan/advice round-trip.
type savedTripBody struct {
	Name   string          `json:"name"`
	Inputs json.RawMessage `json:"inputs"`
	Plan   json.RawMessage `json:"plan,omitempty"`
	Advice json.RawMessage `json:"advice,omitempty"`
}

func handleSavedTripsList(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		out, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []trips.SavedTrip{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleSavedTripCreate(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		var b savedTripBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if len(b.Inputs) == 0 {
			http.Error(w, "inputs required", http.StatusBadRequest)
			return
		}
		t, err := store.Create(r.Context(), name, b.Inputs, b.Plan, b.Advice)
		if err != nil {
			// Surface the unique (user_id, name) violation as 409 so
			// the SPA can prompt "name already exists, overwrite?".
			if isUniqueViolation(err) {
				http.Error(w, "trip name already in use", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, t)
	}
}

func handleSavedTripUpdate(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var b savedTripBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if len(b.Inputs) == 0 {
			http.Error(w, "inputs required", http.StatusBadRequest)
			return
		}
		t, err := store.Update(r.Context(), id, name, b.Inputs, b.Plan, b.Advice)
		if errors.Is(err, trips.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			if isUniqueViolation(err) {
				http.Error(w, "trip name already in use", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func handleSavedTripDelete(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		err = store.Delete(r.Context(), id)
		if errors.Is(err, trips.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// isUniqueViolation returns true for Postgres SQLSTATE 23505 so the
// saved-trips handlers can map "name already in use" to 409 without
// pulling in a dedicated pq error import. lib/pq + pgx both surface
// the code in the error string when wrapped in driver.Value Scanner;
// the canonical match is the pgconn.PgError.Code path. To stay driver-
// agnostic we string-match on the SQLSTATE prefix.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLSTATE 23505") ||
		strings.Contains(err.Error(), "unique constraint") ||
		strings.Contains(err.Error(), "duplicate key value")
}
