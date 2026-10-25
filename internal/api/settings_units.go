package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/insights"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
)

// The account's display units (mi or km, °C or °F), one setting for the web and every iPhone screen. GET reads it; PATCH
// changes it (the iPhone app may, see app_write_allowlist.go). The same resolved object is in GET /api/auth/me as `units`.

// handleUnitsGet: GET /api/settings/units.
func handleUnitsGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := settings.GetUnits(r.Context(), store)
		if err != nil {
			slog.ErrorContext(r.Context(), "units read failed", "err", err.Error())
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, u)
	}
}

// unitsPatch is the body of PATCH /api/settings/units.
type unitsPatch struct {
	DistanceUnit    *string `json:"distance_unit"`
	TemperatureUnit *string `json:"temperature_unit"`
	// Seed: record a client's regional distance default only if the account has none yet (first writer wins).
	Seed bool `json:"seed"`
}

func decodeUnitsPatch(w http.ResponseWriter, r *http.Request) (unitsPatch, string) {
	var p unitsPatch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, "invalid body: " + err.Error()
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return p, "invalid body: one JSON object only"
	}
	if p.DistanceUnit == nil && p.TemperatureUnit == nil {
		return p, "nothing to change: send distance_unit and/or temperature_unit"
	}
	if p.DistanceUnit != nil && *p.DistanceUnit != settings.DistanceMi && *p.DistanceUnit != settings.DistanceKm {
		return p, "distance_unit must be mi or km"
	}
	if t := p.TemperatureUnit; t != nil && *t != settings.TempC && *t != settings.TempF && *t != settings.TempAuto {
		return p, "temperature_unit must be c, f or auto"
	}
	if p.Seed && (p.DistanceUnit == nil || p.TemperatureUnit != nil) {
		return p, "seed takes distance_unit alone"
	}
	return p, ""
}

// handleUnitsPatch: PATCH /api/settings/units. A person's choice replaces anything stored; seed:true only fills an unset
// account. One audit line per write: who, from where, which fields, never the values.
func handleUnitsPatch(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, bad := decodeUnitsPatch(w, r)
		if bad != "" {
			http.Error(w, bad, http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		var err error
		if p.Seed {
			_, err = settings.SeedDistanceUnit(r.Context(), store, *p.DistanceUnit)
		} else {
			err = settings.PatchUnits(r.Context(), store, p.DistanceUnit, p.TemperatureUnit)
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "settings write failed", "group", "display.units", "err", err.Error())
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		source := "web"
		if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp {
			source = "app"
		}
		uid, _ := auth.UserFromContext(r.Context())
		fields := []string{}
		if p.DistanceUnit != nil {
			fields = append(fields, "distance_unit")
		}
		if p.TemperatureUnit != nil {
			fields = append(fields, "temperature_unit")
		}
		slog.InfoContext(r.Context(), "settings write", "user_id", uid.String(), "source", source, "group", "display.units", "fields", fields, "seed", p.Seed)
		u, err := settings.GetUnits(r.Context(), store)
		if err != nil {
			http.Error(w, "saved, but could not read back", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

// unitsFor is the resolver for the `units` object of GET /api/auth/me: nil without a settings store (the object is then left
// out), and the placeholder defaults if the read fails (it is logged by the units route, not here).
func (d Deps) unitsFor() func(ctx context.Context, uid uuid.UUID) *settings.Units {
	if d.Settings == nil {
		return nil
	}
	return func(ctx context.Context, uid uuid.UUID) *settings.Units {
		u, _ := settings.GetUnits(ctx, d.Settings.For(uid))
		return &u
	}
}

// defaultUnitsParam fills in the `units` query parameter of the insight endpoints (efficiency, trends, cold range) from the
// account's setting when the request does not carry a valid one: km gives metric, anything else imperial. An explicit
// `units=imperial` or `units=metric` is left alone, so it stays an override; an unrecognised value counts as absent. The
// handlers behind it are unchanged. Those endpoints bundle temperature with distance in one switch, so a temperature
// choice that differs from the distance one is not honoured there yet (distance decides).
func defaultUnitsParam(lookup func(ctx context.Context, uid uuid.UUID) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if u := q.Get("units"); u == string(insights.Imperial) || u == string(insights.Metric) {
				next.ServeHTTP(w, r)
				return
			}
			uid, ok := auth.UserFromContext(r.Context())
			if !ok || lookup == nil {
				next.ServeHTTP(w, r)
				return
			}
			q.Set("units", lookup(r.Context(), uid))
			r2 := r.Clone(r.Context())
			r2.URL.RawQuery = q.Encode()
			next.ServeHTTP(w, r2)
		})
	}
}

// accountUnitsLookup is the middleware's lookup for these Deps: "metric" when the account's distance unit is km, else
// "imperial" (also when the settings cannot be read or there is no store).
func (d Deps) accountUnitsLookup() func(ctx context.Context, uid uuid.UUID) string {
	if d.Settings == nil {
		return nil
	}
	return func(ctx context.Context, uid uuid.UUID) string {
		if u, _ := settings.GetUnits(ctx, d.Settings.For(uid)); u.DistanceUnit == settings.DistanceKm {
			return string(insights.Metric)
		}
		return string(insights.Imperial)
	}
}
