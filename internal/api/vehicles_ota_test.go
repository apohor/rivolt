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
		"veh-a": json.RawMessage(`{"current":{"version":"2026.30.0","has_notes":true},"available":{"version":"2026.38.1"},"fetched_at":"2026-10-17T10:00:00Z"}`),
		"veh-c": json.RawMessage(`not json`),
	}
	attachOTA(vs, stored, func(id string) *rivian.State {
		if id == "veh-a" {
			return &rivian.State{OtaStatus: "ready_to_install", OtaCurrentVersion: "2026.31.0"}
		}
		return nil
	})
	a := vs[0].OTA
	if a == nil || a.Current == nil || a.Current.Version != "2026.30.0" || !a.Current.HasNotes ||
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

// getOTAUpdateDetails fails for the R2s, so nothing is stored for them: the
// live state's versions (Parallax ota.deployment.state) stand in, without
// release-notes links. Stored details still win where they exist.
func TestAttachOTAFallsBackToLiveVersions(t *testing.T) {
	vs := []rivian.Vehicle{{ID: "r2"}, {ID: "r2-pending"}, {ID: "r1"}, {ID: "none"}}
	stored := map[string]json.RawMessage{
		"r1": json.RawMessage(`{"current":{"version":"2026.36.0","has_notes":true},"fetched_at":"2026-10-17T10:00:00Z"}`),
	}
	live := map[string]*rivian.State{
		"r2":         {OtaCurrentVersion: "2026.36.41", OtaAvailableVersion: "2026.36.41", OtaStatus: "idle"},
		"r2-pending": {OtaCurrentVersion: "2026.36.41", OtaAvailableVersion: "2026.40.2"},
		"r1":         {OtaCurrentVersion: "2026.35.0"},
		"none":       {},
	}
	attachOTA(vs, stored, func(id string) *rivian.State { return live[id] })
	if o := vs[0].OTA; o == nil || o.Current == nil || o.Current.Version != "2026.36.41" || o.Current.HasNotes ||
		o.Available != nil || o.Status != "idle" {
		t.Errorf("r2 ota = %+v", o)
	}
	if o := vs[1].OTA; o == nil || o.Available == nil || o.Available.Version != "2026.40.2" {
		t.Errorf("r2 with a pending update: %+v", o)
	}
	if o := vs[2].OTA; o == nil || o.Current.Version != "2026.36.0" || !o.Current.HasNotes {
		t.Errorf("stored details must win: %+v", o.Current)
	}
	if vs[3].OTA != nil {
		t.Errorf("no stored row and no live version: want no ota, got %+v", vs[3].OTA)
	}
}

// install_progress is served only while the live status is "installing", on the 0 to 100 scale prod's otaInstallProgress uses; any other
// status, known or not, serves none, so the clients never show a bar for a state we cannot name.
func TestAttachOTAServesInstallProgressOnlyWhileInstalling(t *testing.T) {
	stored := map[string]json.RawMessage{}
	for _, id := range []string{"a", "b", "c", "d"} {
		stored[id] = json.RawMessage(`{"current":{"version":"2026.30.0"},"fetched_at":"2026-10-17T10:00:00Z"}`)
	}
	vs := []rivian.Vehicle{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	live := map[string]*rivian.State{
		"a": {OtaStatus: "installing", OtaInstallProgress: 37},
		"b": {OtaStatus: "ready_to_install", OtaInstallProgress: 100},
		"c": {OtaStatus: "verifying", OtaInstallProgress: 90},
		"d": {OtaStatus: "installing"}, // just started: 0, still served
	}
	attachOTA(vs, stored, func(id string) *rivian.State { return live[id] })
	if p := vs[0].OTA.InstallProgress; p == nil || *p != 37 {
		t.Errorf("installing: %v", p)
	}
	if vs[1].OTA.InstallProgress != nil || vs[2].OTA.InstallProgress != nil {
		t.Errorf("a status other than installing served progress: %v %v", vs[1].OTA.InstallProgress, vs[2].OTA.InstallProgress)
	}
	if p := vs[3].OTA.InstallProgress; p == nil || *p != 0 {
		t.Errorf("just started: %v", p)
	}
	out, _ := json.Marshal(vs[0].OTA)
	if !strings.Contains(string(out), `"install_progress":37`) {
		t.Errorf("wire name: %s", out)
	}
	out, _ = json.Marshal(vs[1].OTA)
	if strings.Contains(string(out), "install_progress") {
		t.Errorf("the key must be absent when there is none: %s", out)
	}
}
