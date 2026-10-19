package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/settings"
)

// requireAIConsentMW refuses the LLM-backed endpoints until the user switched
// AI advice on in Settings. It runs before the budget middleware so a refused
// request costs nothing, and it fails closed: no user, no store or a read error
// all mean "off". The answer is 403 {class:"ai_off"} so the SPA can say "Turn
// on AI advice in Settings" instead of failing.
func requireAIConsentMW(sf *settings.Factory) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			if !ok || sf == nil || !settings.AIConsent(r.Context(), sf.For(uid)) {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": "AI advice is off. Turn it on in Settings.",
					"class": "ai_off",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func handleAIConsentGet(sf *settings.Factory) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": settings.AIConsent(r.Context(), sf.For(uid))})
	}
}

func handleAIConsentPut(sf *settings.Factory) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		// Agreeing to send data to an AI provider is the user's own act, never
		// an admin's while viewing as them.
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
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
		if err := settings.SetAIConsent(r.Context(), store, *body.Enabled); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": *body.Enabled})
	}
}
