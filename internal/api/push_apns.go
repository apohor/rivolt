package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/push"
)

// The iPhone app's push endpoints. They take the app's bearer token only: a
// browser session has no business registering an Apple device token, and the
// device is tied to the app session it registered with (it stops receiving
// when that session is revoked or expires).

// apnsGuard refuses what the app endpoints never serve: a request that did
// not come with an app bearer token, one viewing as another user, and a
// server with APNs off.
func apnsGuard(svc *push.Service, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if !svc.APNsEnabled() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not available"})
		return uuid.Nil, false
	}
	ref, ok := auth.SessionFromContext(r.Context())
	if !auth.ViaBearer(r.Context()) || !ok || auth.IsImpersonating(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "app sign-in required"})
		return uuid.Nil, false
	}
	return ref.ID, true
}

func decodeAPNsBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return false
	}
	return true
}

func handleAPNsRegister(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid, ok := apnsGuard(svc, w, r)
		if !ok {
			return
		}
		var body struct {
			Token    string `json:"token"`
			Env      string `json:"env"`
			BundleID string `json:"bundle_id"`
		}
		if !decodeAPNsBody(w, r, &body) {
			return
		}
		topic := svc.APNsTopic()
		if !push.ValidDeviceToken(body.Token) || (body.Env != "sandbox" && body.Env != "production") ||
			(body.BundleID != "" && body.BundleID != topic) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid device"})
			return
		}
		if err := store.UpsertAPNsDevice(r.Context(), sid, push.APNsDevice{Token: body.Token, Env: body.Env, BundleID: topic}); err != nil {
			slog.ErrorContext(r.Context(), "apns register", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		prefs, _, err := store.APNsPrefs(r.Context(), body.Token)
		if err != nil {
			slog.ErrorContext(r.Context(), "apns register prefs", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		live, _, err := store.APNsLive(r.Context(), body.Token)
		if err != nil {
			slog.ErrorContext(r.Context(), "apns register live", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preferences": apnsPrefsJSON{Preferences: prefs, OnChargingLive: live}})
	}
}

func handleAPNsPreferences(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := apnsGuard(svc, w, r); !ok {
			return
		}
		var body struct {
			Token            string `json:"token"`
			OnChargingDone   *bool  `json:"on_charging_done"`
			OnAnomaly        *bool  `json:"on_anomaly"`
			OnSoftwareUpdate *bool  `json:"on_software_update"`
			OnPlugInReminder *bool  `json:"on_plug_in_reminder"`
			OnMilestones     *bool  `json:"on_milestones"`
			OnChargingLive   *bool  `json:"on_charging_live"`
		}
		if !decodeAPNsBody(w, r, &body) {
			return
		}
		if !push.ValidDeviceToken(body.Token) ||
			(body.OnChargingDone == nil && body.OnAnomaly == nil && body.OnSoftwareUpdate == nil &&
				body.OnPlugInReminder == nil && body.OnMilestones == nil && body.OnChargingLive == nil) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token and at least one switch required"})
			return
		}
		found, err := store.SetAPNsPrefs(r.Context(), body.Token, body.OnChargingDone, body.OnAnomaly, body.OnSoftwareUpdate)
		if err == nil && (body.OnPlugInReminder != nil || body.OnMilestones != nil) {
			var f2 bool
			f2, err = store.SetAPNsOptIns(r.Context(), body.Token, body.OnPlugInReminder, body.OnMilestones)
			found = found || f2
		}
		if err == nil && body.OnChargingLive != nil {
			var f3 bool
			f3, err = store.SetAPNsLive(r.Context(), body.Token, *body.OnChargingLive)
			found = found || f3
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "apns preferences", "err", err.Error())
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

func handleAPNsUnregister(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := apnsGuard(svc, w, r); !ok {
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if !decodeAPNsBody(w, r, &body) {
			return
		}
		if !push.ValidDeviceToken(body.Token) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid device"})
			return
		}
		if err := store.DeleteAPNsDevice(r.Context(), body.Token); err != nil {
			slog.ErrorContext(r.Context(), "apns unregister", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handlePushNotification serves the text of one notification to the app's
// Notification Service Extension. Apple only ever carried the id; the text is
// the signed-in user's own and kept for 24 hours.
func handlePushNotification(store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		n, err := store.GetNotification(r.Context(), id)
		if errors.Is(err, push.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "push notification", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, n)
	}
}

// handleAPNsTest: POST /api/push/apns/test sends a fixed test notification to
// the caller's own signed-in iPhones (any session, so the owner can try it from
// the browser too). Own user only, rate-limited by the route; nothing in the
// request reaches Apple or the phone.
func handleAPNsTest(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !svc.APNsEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not available"})
			return
		}
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
			return
		}
		n := svc.SendAPNsTest(r.Context(), store)
		if n == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no iPhone to send to"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": n})
	}
}

// apnsPrefsJSON is a device's switches as the app reads them: the five the web has,
// plus the iPhone-only Live Activity switch.
type apnsPrefsJSON struct {
	push.Preferences
	OnChargingLive bool `json:"on_charging_live"`
}

// handleAPNsActivity: POST /api/push/apns/activity {token, activity_token,
// vehicle_id} records a running Live Activity's push token. 409 while the
// device's Live Activity switch is off; 404 when the device or vehicle is not the
// caller's. The app starts the activity itself; the server never starts one.
func handleAPNsActivity(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := apnsGuard(svc, w, r); !ok {
			return
		}
		var body struct {
			Token         string `json:"token"`
			ActivityToken string `json:"activity_token"`
			VehicleID     string `json:"vehicle_id"`
		}
		if !decodeAPNsBody(w, r, &body) {
			return
		}
		vid, verr := uuid.Parse(body.VehicleID)
		if !push.ValidDeviceToken(body.Token) || !push.ValidActivityToken(body.ActivityToken) || verr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}
		switch err := store.RegisterActivity(r.Context(), body.Token, body.ActivityToken, vid); {
		case errors.Is(err, push.ErrLiveOff):
			writeJSON(w, http.StatusConflict, map[string]any{"error": "live charging is off for this device"})
		case errors.Is(err, push.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		case err != nil:
			slog.ErrorContext(r.Context(), "apns activity", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	}
}

// handleAPNsActivityEnd: POST /api/push/apns/activity/end {activity_token} forgets
// an activity token. 200 whether or not it was known.
func handleAPNsActivityEnd(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := apnsGuard(svc, w, r); !ok {
			return
		}
		var body struct {
			ActivityToken string `json:"activity_token"`
		}
		if !decodeAPNsBody(w, r, &body) {
			return
		}
		if !push.ValidActivityToken(body.ActivityToken) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}
		if err := store.EndActivity(r.Context(), body.ActivityToken); err != nil {
			slog.ErrorContext(r.Context(), "apns activity end", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleAPNsActivityTest: POST /api/push/apns/activity/test sends fixed figures to
// the caller's own running Live Activities, then (a few seconds later) the end. Own
// user only, rate-limited by the route, 404 while Live Activity is off or nothing is
// registered. Nothing in the request reaches Apple or the phone.
func handleAPNsActivityTest(svc *push.Service, store *push.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !svc.LiveEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not available"})
			return
		}
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
			return
		}
		n := svc.TestLive(r.Context(), store)
		if n == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no live charging display to send to"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": n})
	}
}
