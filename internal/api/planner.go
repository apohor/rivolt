package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// homeLocationResponse is the saved home plus what the Settings card needs around it: whether there's an earlier
// home to restore, and whether a street address can be looked up (self-hosted Photon only).
type homeLocationResponse struct {
	settings.HomeLocation
	PreviousSet   bool `json:"previous_set"`
	AddressSearch bool `json:"address_search"`
}

func homeLocationOut(ctx context.Context, store *settings.Store, photon *geocoding.PhotonClient) (homeLocationResponse, error) {
	h, err := settings.GetHomeLocation(ctx, store)
	if err != nil {
		return homeLocationResponse{}, err
	}
	prev, err := settings.PreviousHomeLocation(ctx, store)
	if err != nil {
		return homeLocationResponse{}, err
	}
	return homeLocationResponse{HomeLocation: h, PreviousSet: prev.Set, AddressSearch: photon.Enabled()}, nil
}

// handleHomeLocationGet returns the user's saved home location, or
// {"set": false} when none is configured. Used by the trip planner
// to decide whether to render the "Home" preset.
func handleHomeLocationGet(store *settings.Store, photon *geocoding.PhotonClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := homeLocationOut(r.Context(), store, photon)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// homeChangedFields names what a save changes, for the audit line (never the values: no coordinates or address).
func homeChangedFields(old, h settings.HomeLocation) []string {
	var f []string
	if old.Set != h.Set {
		f = append(f, "set")
	}
	if old.Latitude != h.Latitude || old.Longitude != h.Longitude {
		f = append(f, "latitude", "longitude")
	}
	if old.Label != h.Label && h.Label != "" {
		f = append(f, "label")
	}
	if old.Address != h.Address {
		f = append(f, "address")
	}
	if old.Source != h.Source {
		f = append(f, "source")
	}
	return f
}

// handleHomeLocationPut writes the user's home location (web only: not on the app's write allowlist). Body shape
// matches HomeLocation; Set=false clears it. A latitude or longitude that isn't finite and in range is a 400. The
// home it replaces is kept for "restore". The audit line names the fields changed, never their values.
func handleHomeLocationPut(store *settings.Store, photon *geocoding.PhotonClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var h settings.HomeLocation
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&h); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := settings.ValidateHomeLocation(h); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		old, _ := settings.GetHomeLocation(r.Context(), store)
		if err := settings.SetHomeLocation(r.Context(), store, h); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.InfoContext(r.Context(), "home location saved", "fields", homeChangedFields(old, h), "source", h.Source)
		// Echo back what's now stored so the SPA can update its in-memory copy without a second GET.
		out, _ := homeLocationOut(r.Context(), store, photon)
		writeJSON(w, http.StatusOK, out)
	}
}

// handleHomeLocationRestore serves POST /api/settings/home-location/restore: back to the home the latest change
// replaced (404 when there's none). Web only, audited like a save.
func handleHomeLocationRestore(store *settings.Store, photon *geocoding.PhotonClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := settings.RestorePreviousHomeLocation(r.Context(), store); err != nil {
			if errors.Is(err, settings.ErrNoPreviousHome) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "nothing to restore"})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.InfoContext(r.Context(), "home location restored", "fields", []string{"latitude", "longitude", "label", "address", "source"})
		out, _ := homeLocationOut(r.Context(), store, photon)
		writeJSON(w, http.StatusOK, out)
	}
}

// handleHomeAddressSearch serves POST /api/settings/home-location/search {"q": "..."}: a street address through the
// self-hosted Photon only, never Open-Meteo or the planner's geocode path. 404 when Photon isn't configured (the
// card then hides the address field). The address travels in the body, not the URL, and is never logged; an upstream
// error is a bare 502 (Photon's error carries the URL).
func handleHomeAddressSearch(photon *geocoding.PhotonClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !photon.Enabled() {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "address search isn't available on this server"})
			return
		}
		var body struct {
			Q string `json:"q"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		q := strings.TrimSpace(body.Q)
		if len(q) < 3 {
			writeJSON(w, http.StatusOK, []geocoding.Result{})
			return
		}
		res, err := photon.Search(r.Context(), q, 5)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "address search failed"})
			return
		}
		if res == nil {
			res = []geocoding.Result{}
		}
		writeJSON(w, http.StatusOK, res)
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
		if err := validateSavedPlan(b.Plan); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
		if err := validateSavedPlan(b.Plan); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
