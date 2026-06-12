package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/settings"
)

// handleAISettingsGet returns the redacted AI configuration.
func handleAISettingsGet(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, mgr.Public())
	}
}

// handleAISettingsPut accepts a partial patch: nil fields are untouched,
// empty-string fields clear, non-empty values update.
func handleAISettingsPut(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		var patch settings.AIUpdate
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
		pub, err := mgr.Update(r.Context(), patch)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pub)
	}
}

// handleRecapSettingsGet returns the current recap configuration.
func handleRecapSettingsGet(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, mgr.RecapPublic())
	}
}

// handleRecapSettingsPut accepts a partial patch for recap settings.
func handleRecapSettingsPut(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		var patch settings.RecapUpdate
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
		pub, err := mgr.UpdateRecap(r.Context(), patch)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pub)
	}
}

// handleGPSSettingsGet returns the GPS accuracy thresholds.
func handleGPSSettingsGet(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, mgr.GPSPublic())
	}
}

// handleGPSSettingsPut accepts a partial patch for GPS thresholds.
func handleGPSSettingsPut(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		var patch settings.GPSUpdate
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
		pub, err := mgr.UpdateGPS(r.Context(), patch)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pub)
	}
}

// handleAIModelsList proxies the provider's catalogue endpoint using the
// stored API key so the UI can offer a live dropdown instead of asking
// users to remember model IDs that drift across releases.
func handleAIModelsList(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		provider := chi.URLParam(r, "provider")
		// Independent timeout: the provider list endpoints are small but
		// we don't want to inherit a stalled request's context.
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		models, err := mgr.ListModels(ctx, provider)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	}
}

// handleAIPing sends a trivial prompt to the currently configured
// provider and returns the reply along with token usage and
// round-trip latency. Two goals:
//   - Let the Settings UI confirm the provider/key/model triple is
//     valid without waiting for a downstream feature to exercise it.
//   - Surface real error messages from the provider verbatim (wrong
//     key, expired credit, model not available on account, etc.)
//     so the operator can self-diagnose.
//
// The prompt is intentionally minimal — we bill the user's account
// for each ping, so we want to spend the fewest possible tokens.
// Replies cap at ~20 tokens in practice because we ask for one
// short sentence; we still log input/output token counts for
// transparency.
func handleAIPing(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		analyzer := mgr.Analyzer()
		if analyzer == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no AI provider configured — add an API key in Settings → AI providers",
			})
			return
		}
		// Hard cap the outbound call at 20s. Provider completion APIs
		// typically respond in 1-3s for a 20-token answer; anything
		// beyond that points to an outage or a wedged model, and we
		// don't want the button spinning forever.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		const system = "You are a connectivity smoke test. Answer in one short sentence only."
		const user = "Reply with a single sentence confirming that this integration works."
		start := time.Now()
		reply, usage, err := analyzer.Complete(ctx, system, user)
		latency := time.Since(start)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":      err.Error(),
				"model":      analyzer.ModelName(),
				"latency_ms": latency.Milliseconds(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"reply":         strings.TrimSpace(reply),
			"model":         analyzer.ModelName(),
			"latency_ms":    latency.Milliseconds(),
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		})
	}
}
