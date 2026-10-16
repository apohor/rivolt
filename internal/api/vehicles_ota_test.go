package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestAttachOTAAddsStoredDetailsAndLiveStatus(t *testing.T) {
	vs := []rivian.Vehicle{{ID: "veh-a"}, {ID: "veh-b"}, {ID: "veh-c"}}
	stored := map[string]json.RawMessage{
		"veh-a": json.RawMessage(`{"current":{"version":"2026.30.0","release_notes_url":"https://rivian.com/n"},"available":{"version":"2026.38.1"},"fetched_at":"2026-10-17T10:00:00Z"}`),
		"veh-c": json.RawMessage(`not json`),
	}
	attachOTA(vs, stored, func(id string) string {
		if id == "veh-a" {
			return "ready_to_install"
		}
		return ""
	})
	a := vs[0].OTA
	if a == nil || a.Current == nil || a.Current.Version != "2026.30.0" || a.Current.ReleaseNotesURL != "https://rivian.com/n" ||
		a.Available == nil || a.Available.Version != "2026.38.1" || a.Status != "ready_to_install" {
		t.Fatalf("veh-a ota = %+v", a)
	}
	if vs[1].OTA != nil || vs[2].OTA != nil {
		t.Errorf("a vehicle with nothing stored, or with a row that does not decode, must have no ota: %+v %+v", vs[1].OTA, vs[2].OTA)
	}
	out, _ := json.Marshal(vs[1])
	if strings.Contains(string(out), `"ota"`) {
		t.Errorf("a vehicle without OTA must omit the key: %s", out)
	}
}
