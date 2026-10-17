package api

import (
	"encoding/json"
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
