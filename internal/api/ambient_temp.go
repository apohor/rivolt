package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/weather"
)

// Outside temperature. Rivian reports none (vehicle_state.outside_temp_c is 0 on
// every row), so a user who opts in gets it looked up from Open-Meteo. The rules
// (DA's ruling on #617):
//   - a separate opt-in, off by default, not the drive-weather toggle;
//   - looked up only while the app is open, from GET /api/vehicles/{id}/now, never
//     by a background poll;
//   - the position is rounded to about 11 km before the cache key and before the
//     request, and a cached value is reused for ambientTTL;
//   - no coordinates in logs (an HTTP client's error text carries the request URL,
//     so errors are logged by class only);
//   - nothing for the demo user; toggle off means no call.

const (
	ambientTTL     = 30 * time.Minute
	ambientTimeout = 2 * time.Second
	ambientSource  = "open-meteo"
)

// ambientLookup returns the /now seam. nil client or cache turns the feature off.
func ambientLookup(optedIn func(context.Context, uuid.UUID) bool, client *weather.Client, cache *weather.MemCache, logger *slog.Logger, isDemoUser func(uuid.UUID) bool) func(context.Context, uuid.UUID, float64, float64) *nowAmbient {
	if optedIn == nil || client == nil || cache == nil {
		return nil
	}
	return func(ctx context.Context, uid uuid.UUID, lat, lon float64) *nowAmbient {
		if isDemoUser != nil && isDemoUser(uid) {
			return nil
		}
		if !optedIn(ctx, uid) {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, ambientTimeout)
		defer cancel()
		snap, hour, err := cache.FetchHourCached(ctx, client, lat, lon, time.Now(), 0, false)
		if err != nil || snap == nil || !snap.HasTemp {
			if logger != nil {
				// The error text would carry the request URL, coordinates included.
				logger.Debug("ambient temperature unavailable", "class", ambientErrClass(err))
			}
			return nil
		}
		return &nowAmbient{TempC: snap.TempC, Source: ambientSource, AsOf: hour.UTC()}
	}
}

func ambientErrClass(err error) string {
	switch {
	case err == nil:
		return "no_value"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "upstream"
	}
}

func isDemoUser(uid uuid.UUID) bool { return demo.Enabled() && uid == demo.UserID() }

// GET/PUT /api/settings/ambient-temp {enabled}. Turning a location lookup on is
// the user's own act, never an admin's while viewing as them.
func handleAmbientTempGet(sf *settings.Factory) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": settings.AmbientTempOptIn(r.Context(), sf.For(uid))})
	}
}

func handleAmbientTempPut(sf *settings.Factory) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
			return
		}
		if isDemoUser(uid) {
			http.Error(w, "not available in the demo", http.StatusForbidden)
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		store := sf.For(uid)
		if store == nil {
			http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := settings.SetAmbientTempOptIn(r.Context(), store, *body.Enabled); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": *body.Enabled})
	}
}

// newAmbientCache is the cache for the lookups: one entry per (rounded cell, UTC
// hour), kept ambientTTL, so repeated /now polls and users in the same cell cost
// one upstream call per half hour.
func newAmbientCache() *weather.MemCache { return weather.NewMemCache(ambientTTL, 512) }

// ambientOptIn reads the user's choice from their settings (nil: never).
func ambientOptIn(sf *settings.Factory) func(context.Context, uuid.UUID) bool {
	if sf == nil {
		return nil
	}
	return func(ctx context.Context, uid uuid.UUID) bool { return settings.AmbientTempOptIn(ctx, sf.For(uid)) }
}
