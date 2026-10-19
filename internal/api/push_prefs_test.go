package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/push"
)

// A subscribe body from a client that predates the software-update toggle
// must leave it on, while the toggles it does send are honoured.
func TestSubscribePreferencesDefaultNewToggleOn(t *testing.T) {
	var body pushSubscribeRequest
	if err := json.Unmarshal([]byte(`{"preferences":{"on_charging_done":false,"on_plug_in_reminder":true,"on_anomaly":true}}`), &body); err != nil {
		t.Fatal(err)
	}
	prefs := push.Preferences{OnChargingDone: true, OnPlugInReminder: true, OnAnomaly: true, OnSoftwareUpdate: true}
	if err := json.Unmarshal(body.Preferences, &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.OnChargingDone || !prefs.OnSoftwareUpdate {
		t.Errorf("prefs = %+v", prefs)
	}
}

// A subscription for an endpoint that is not a push service is refused before it
// is stored (the store is nil: reaching it would panic).
func TestSubscribeRefusesNonPushEndpoints(t *testing.T) {
	for _, ep := range []string{"http://fcm.googleapis.com/x", "https://169.254.169.254/latest", "https://kubernetes.default.svc/", "https://evil.example/p"} {
		body := `{"endpoint":"` + ep + `","keys":{"p256dh":"k","auth":"a"}}`
		rec := httptest.NewRecorder()
		handlePushSubscribe(nil)(rec, httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", ep, rec.Code)
		}
	}
}
