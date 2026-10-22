package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
)

// handleChargingSettingsGet returns the operator-configured home
// electricity cost settings. Safe to call even when the settings
// store is unavailable — returns defaults.
func handleChargingSettingsGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := settings.GetChargingConfig(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	}
}

// handleChargingSettingsPut persists new home-charging cost settings.
// Accepts { "home_price_per_kwh": <number>, "home_currency": "USD" }.
// Empty currency is coerced to the default in the settings layer.
func handleChargingSettingsPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		var cfg settings.ChargingConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetChargingConfig(r.Context(), store, cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Re-read so the response reflects any defaults filled in.
		out, err := settings.GetChargingConfig(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleChargingNetworksGet returns the user's price book for fast /
// public charging networks. Empty list when nothing is configured.
func handleChargingNetworksGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nets, err := settings.GetChargingNetworks(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if nets == nil {
			nets = []settings.ChargingNetwork{}
		}
		writeJSON(w, http.StatusOK, nets)
	}
}

// handleChargingNetworksPut overwrites the price book with the
// provided list. The settings layer drops invalid rows; the response
// reflects the post-normalization state so the UI re-syncs.
func handleChargingNetworksPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		var nets []settings.ChargingNetwork
		if err := json.NewDecoder(r.Body).Decode(&nets); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.ValidateNetworkLocations(nets); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetChargingNetworks(r.Context(), store, nets); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := settings.GetChargingNetworks(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []settings.ChargingNetwork{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleChargingNetworksReset clears the user's persisted price book
// so the next Get returns the seed defaults from dcfcrates.Networks.
// Drives the "Reset to defaults" button on the Settings panel.
func handleChargingNetworksReset(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := settings.ResetChargingNetworks(r.Context(), store); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := settings.GetChargingNetworks(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// chargingRatePatch is the body of PATCH /api/settings/charging: only the home rate.
type chargingRatePatch struct {
	HomePricePerKWh *float64 `json:"home_price_per_kwh"`
	HomeCurrency    *string  `json:"home_currency"`
}

// decodeChargingRatePatch reads and checks the body: one JSON object of known fields
// (anything else, a second value after it, or an empty object is refused), a rate from
// 0 to settings.MaxHomePricePerKWh and a currency from settings.HomeCurrencies. The
// currency comes back normalised to upper case.
func decodeChargingRatePatch(w http.ResponseWriter, r *http.Request) (chargingRatePatch, string) {
	var p chargingRatePatch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, "invalid body: " + err.Error()
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return p, "invalid body: one JSON object only"
	}
	if p.HomePricePerKWh == nil && p.HomeCurrency == nil {
		return p, "nothing to change: send home_price_per_kwh and/or home_currency"
	}
	if v := p.HomePricePerKWh; v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > settings.MaxHomePricePerKWh) {
		return p, "home_price_per_kwh must be a number from 0 to 5"
	}
	if p.HomeCurrency != nil {
		c, ok := settings.NormalizeHomeCurrency(*p.HomeCurrency)
		if !ok {
			return p, "home_currency must be one of USD, CAD, EUR, GBP, AUD, NZD, CHF, SEK, NOK, DKK"
		}
		p.HomeCurrency = &c
	}
	return p, ""
}

// handleChargingSettingsPatch: PATCH /api/settings/charging changes only the home rate
// fields it carries and leaves everything else as stored (the full-overwrite PUT stays
// for the web). It is the one settings write the iPhone app may make. The body is
// strict (see decodeChargingRatePatch). Applying a rate to past charges is a separate
// web-only route. One audit line per write: who, from where, which group and fields,
// never the values.
func handleChargingSettingsPatch(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, bad := decodeChargingRatePatch(w, r)
		if bad != "" {
			http.Error(w, bad, http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "settings store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := settings.PatchChargingConfig(r.Context(), store, p.HomePricePerKWh, p.HomeCurrency); err != nil {
			slog.ErrorContext(r.Context(), "settings write failed", "group", "charging.home_rate", "err", err.Error())
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		source := "web"
		if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp {
			source = "app"
		}
		uid, _ := auth.UserFromContext(r.Context())
		fields := []string{}
		if p.HomePricePerKWh != nil {
			fields = append(fields, "home_price_per_kwh")
		}
		if p.HomeCurrency != nil {
			fields = append(fields, "home_currency")
		}
		slog.InfoContext(r.Context(), "settings write", "user_id", uid.String(), "source", source, "group", "charging.home_rate", "fields", fields)
		out, err := settings.GetChargingConfig(r.Context(), store)
		if err != nil {
			http.Error(w, "saved, but could not read back", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
