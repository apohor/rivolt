package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/apohor/rivolt/internal/auth"
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
	Preferences json.RawMessage `json:"preferences,omitempty"`
	UserAgent   string          `json:"user_agent,omitempty"`
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
		if err := push.ValidEndpoint(body.Endpoint); err != nil {
			// The host only (never the path, which is a capability, or the keys): so the next unfamiliar push service a browser
			// hands out is visible in the logs. It goes back to the caller too, who sent it, so the page can name it.
			host := push.EndpointHost(body.Endpoint)
			slog.WarnContext(r.Context(), "push: subscribe refused, unsupported push service", "host", host, "user_agent", truncateRunes(body.UserAgent, 200))
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported push service", "class": "unsupported_push_service", "host": host})
			return
		}

		// Charging-done, anomaly and software-update default to on: what a
		// subscriber almost certainly expected when they tapped "Enable
		// notifications". The plug-in reminder, milestones and the left-open alert are opt-in only.
		prefs := push.Preferences{OnChargingDone: true, OnAnomaly: true, OnSoftwareUpdate: true}
		if len(body.Preferences) > 0 && string(body.Preferences) != "null" {
			// Decode over the defaults so a client that predates a toggle
			// (an old cached PWA) leaves it on instead of turning it off.
			if err := json.Unmarshal(body.Preferences, &prefs); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid preferences: " + err.Error()})
				return
			}
		}

		// The plug-in reminder, milestones and the left-open alert are opt-in and are NOT taken from
		// here, whatever the client posts (an old Settings tab or pod would
		// post its stale local defaults): the stored values stay as they are
		// and only POST /api/push/optin changes them.
		prefs.OnPlugInReminder, prefs.OnMilestones, prefs.OnLeftOpen = false, false, false
		sub := push.Subscription{
			Endpoint:         body.Endpoint,
			P256dh:           body.Keys.P256dh,
			Auth:             body.Keys.Auth,
			OnChargingDone:   prefs.OnChargingDone,
			OnAnomaly:        prefs.OnAnomaly,
			OnSoftwareUpdate: prefs.OnSoftwareUpdate,
			UserAgent:        body.UserAgent,
		}
		if store == nil {
			writePushUnavailable(w)
			return
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
			"on_software_update", prefs.OnSoftwareUpdate,
			"on_milestones", prefs.OnMilestones,
			"on_left_open", prefs.OnLeftOpen,
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"preferences": prefs,
		})
	}
}

func handlePushUnsubscribe(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writePushUnavailable(w)
			return
		}
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
		if store == nil {
			writePushUnavailable(w)
			return
		}
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

func handlePushTest(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writePushUnavailable(w)
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "endpoint required",
			})
			return
		}
		if err := svc.SendTest(r.Context(), store, body.Endpoint); err != nil {
			// Include the error verbatim — this is typically the push
			// provider's response (e.g. Apple's BadJwtToken / InvalidToken)
			// and surfacing it in the UI beats generic "Save failed".
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handlePushDevice: POST /api/push/device {endpoint} returns that device's
// stored switches, so the Settings panel shows what the server has instead of
// local defaults. A POST, so the endpoint (a capability URL) stays out of
// access logs. 404 when the endpoint is not one of the caller's.
func handlePushDevice(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writePushUnavailable(w)
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Endpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint required"})
			return
		}
		sub, err := store.Get(r.Context(), body.Endpoint)
		if err != nil {
			slog.ErrorContext(r.Context(), "push device", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		if sub == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"preferences": push.Preferences{
			OnChargingDone:   sub.OnChargingDone,
			OnPlugInReminder: sub.OnPlugInReminder,
			OnAnomaly:        sub.OnAnomaly,
			OnSoftwareUpdate: sub.OnSoftwareUpdate,
			OnMilestones:     sub.OnMilestones,
			OnLeftOpen:       sub.OnLeftOpen,
		}})
	}
}

// handlePushOff: POST /api/push/off {"kind":"plug_in_reminder"|"milestones"|"left_open"} is
// the one-tap stop: it turns that switch off on every device of the account, not
// only the one in hand. Never an admin's act while viewing as the user.
func handlePushOff(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writePushUnavailable(w)
			return
		}
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
			return
		}
		var body struct {
			Kind string `json:"kind"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
			(body.Kind != "plug_in_reminder" && body.Kind != "milestones" && body.Kind != "left_open") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "kind must be plug_in_reminder, milestones or left_open"})
			return
		}
		n, err := store.SwitchOffAll(r.Context(), body.Kind)
		if err != nil {
			slog.ErrorContext(r.Context(), "push off", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices_changed": n})
	}
}

// handlePushOptIn: POST /api/push/optin {endpoint, on_plug_in_reminder?,
// on_milestones?} sets the opt-in switches of one of the caller's devices. It
// is the only way either goes on (subscribe ignores them), and it comes from the
// Settings screen's own control. 404 when the endpoint is not the caller's;
// never an admin's act while viewing as the user.
func handlePushOptIn(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writePushUnavailable(w)
			return
		}
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
			return
		}
		var body struct {
			Endpoint         string `json:"endpoint"`
			OnPlugInReminder *bool  `json:"on_plug_in_reminder"`
			OnMilestones     *bool  `json:"on_milestones"`
			OnLeftOpen       *bool  `json:"on_left_open"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Endpoint == "" ||
			(body.OnPlugInReminder == nil && body.OnMilestones == nil && body.OnLeftOpen == nil) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint and at least one switch required"})
			return
		}
		var found bool
		var err error
		if body.OnPlugInReminder != nil || body.OnMilestones != nil {
			found, err = store.SetOptIns(r.Context(), body.Endpoint, body.OnPlugInReminder, body.OnMilestones)
		}
		if err == nil && body.OnLeftOpen != nil {
			var f2 bool
			f2, err = store.SetLeftOpen(r.Context(), body.Endpoint, *body.OnLeftOpen)
			found = found || f2
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "push optin", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// pushStoreMiddleware answers 503 {"error":"unavailable"} when the signed-in user has no push store (the factory is not wired, or the
// store could not be opened), so a /push handler never dereferences a nil *push.Store. Routes with no signed-in user pass through.
func pushStoreMiddleware(f *push.Factory) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if uid, ok := auth.UserFromContext(r.Context()); ok && f.For(uid) == nil {
				writePushUnavailable(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writePushUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
}
