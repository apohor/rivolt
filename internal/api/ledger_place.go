package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// placeRestamper is the slice of *charges.Store the re-stamp handlers
// use, so they can be tested without Postgres.
type placeRestamper interface {
	RestampPlace(ctx context.Context, o charges.RestampOptions) (charges.RestampResult, error)
	UndoRestamp(ctx context.Context, restampID *uuid.UUID) (charges.UndoResult, error)
}

// restampSettings is what the handlers read from the user's settings.
type restampSettings struct {
	home     func(ctx context.Context) (settings.HomeLocation, error)
	charging func(ctx context.Context) (settings.ChargingConfig, error)
}

// handleRestampPlace serves POST /api/charges/restamp-place[?dry_run=1]:
// after a home-location *correction* ("the old pin was wrong"),
// recompute the recorded home/away place on past charges against the
// currently saved home, and price the AC charges that become home and
// were unpriced at the home rate (scratchpad/place-restamp-contract.md
// v1.1). User-priced rows are never touched.
func handleRestampPlace(store placeRestamper, cfg restampSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "charges disabled"})
			return
		}
		h, err := cfg.home(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if !h.Set {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "no home location saved"})
			return
		}
		cc, err := cfg.charging(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		dry := r.URL.Query().Get("dry_run") == "1" || r.URL.Query().Get("dry_run") == "true"
		res, err := store.RestampPlace(r.Context(), charges.RestampOptions{
			HomeLat: h.Latitude, HomeLon: h.Longitude, RadiusM: rivian.HomeRadiusM,
			HomeRate: cc.HomePricePerKWh, Currency: strings.ToUpper(cc.HomeCurrency),
			IsDCFC: rivian.IsDCFCSession, DryRun: dry,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// handleUndoRestamp serves POST /api/charges/restamp-place/undo
// [?restamp_id=]: revert a correction (the latest when no id is given).
// 404 when there's nothing to undo.
func handleUndoRestamp(store placeRestamper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "charges disabled"})
			return
		}
		var id *uuid.UUID
		if v := strings.TrimSpace(r.URL.Query().Get("restamp_id")); v != "" {
			parsed, err := uuid.Parse(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid restamp_id"})
				return
			}
			id = &parsed
		}
		res, err := store.UndoRestamp(r.Context(), id)
		if errors.Is(err, charges.ErrNoRestamp) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "nothing to undo"})
			return
		}
		if errors.Is(err, charges.ErrRestampNotLatest) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "only the latest correction can be undone"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// restamperFor returns the user's charges store as a placeRestamper,
// nil (not a typed nil) when charges are disabled.
func restamperFor(d Deps, uid uuid.UUID) placeRestamper {
	if d.Charges == nil {
		return nil
	}
	if cs := d.Charges.For(uid); cs != nil {
		return cs
	}
	return nil
}

func restampSettingsFor(d Deps, uid uuid.UUID) restampSettings {
	ss := d.Settings.For(uid)
	return restampSettings{
		home:     func(ctx context.Context) (settings.HomeLocation, error) { return settings.GetHomeLocation(ctx, ss) },
		charging: func(ctx context.Context) (settings.ChargingConfig, error) { return settings.GetChargingConfig(ctx, ss) },
	}
}
