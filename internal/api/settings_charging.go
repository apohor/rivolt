package api

import (
	"encoding/json"
	"net/http"

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
