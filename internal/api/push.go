package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/apohor/rivolt/internal/push"
)

// --- Push notifications ----------------------------------------------------
//
// The PWA's service worker handles the browser-side pieces (asking for
// permission, calling pushManager.subscribe, showing the notification). The
// routes below persist the resulting subscription and let the frontend fetch
// the VAPID public key it needs to subscribe in the first place.

// pushSubscribeRequest mirrors the shape produced by
// PushSubscription.toJSON() in the browser, plus our own preferences
// object so users can opt specific notification types in/out per device.
type pushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Preferences *push.Preferences `json:"preferences,omitempty"`
	UserAgent   string            `json:"user_agent,omitempty"`
}

func handlePushVAPIDKey(svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if svc == nil || svc.PublicKey() == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "push disabled",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"public_key": svc.PublicKey(),
		})
	}
}

func handlePushSubscribe(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body pushSubscribeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid json: " + err.Error(),
			})
			return
		}
		if body.Endpoint == "" || body.Keys.P256dh == "" || body.Keys.Auth == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "endpoint, keys.p256dh, and keys.auth are required",
			})
			return
		}

		// Default both toggles to on so first-time subscribers get the
		// behaviour they almost certainly expected when they tapped
		// "Enable notifications".
		prefs := push.Preferences{OnChargingDone: true, OnPlugInReminder: true, OnAnomaly: true}
		if body.Preferences != nil {
			prefs = *body.Preferences
		}

		sub := push.Subscription{
			Endpoint:         body.Endpoint,
			P256dh:           body.Keys.P256dh,
			Auth:             body.Keys.Auth,
			OnChargingDone:   prefs.OnChargingDone,
			OnPlugInReminder: prefs.OnPlugInReminder,
			OnAnomaly:        prefs.OnAnomaly,
			UserAgent:        body.UserAgent,
		}
		if err := store.Upsert(r.Context(), sub); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// Log the push host + user-agent so it's obvious from container
		// logs which device subscribed to which provider. iOS goes to
		// web.push.apple.com; Chrome to fcm.googleapis.com or its newer
		// variant; Firefox to updates.push.services.mozilla.com.
		host := ""
		if u, err := url.Parse(body.Endpoint); err == nil {
			host = u.Host
		}
		slog.Info("push: subscribed",
			"host", host,
			"user_agent", body.UserAgent,
			"on_charging_done", prefs.OnChargingDone,
			"on_plug_in_reminder", prefs.OnPlugInReminder,
			"on_anomaly", prefs.OnAnomaly,
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"preferences": prefs,
		})
	}
}

func handlePushUnsubscribe(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "endpoint required",
			})
			return
		}
		if err := store.Delete(r.Context(), body.Endpoint); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handlePushStatus reports aggregate push state for the Settings UI. We
// deliberately don't list individual subscriptions: those are opaque
// endpoint URLs that would be meaningless to show.
func handlePushStatus(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := store.Count(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":            svc != nil && svc.PublicKey() != "",
			"subscription_count": n,
		})
	}
}

func handlePushTest(svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "endpoint required",
			})
			return
		}
		if err := svc.SendTest(r.Context(), body.Endpoint); err != nil {
			// Include the error verbatim — this is typically the push
			// provider's response (e.g. Apple's BadJwtToken / InvalidToken)
			// and surfacing it in the UI beats generic "Save failed".
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
