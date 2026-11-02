package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/rivian"
)

// GET /api/state carries the update's step and install percent, derived from the truck's own status and progress with the same rules as
// ota.step / ota.install_progress on /api/vehicles: named steps only, unknown words "unknown", idle absent, the percent only while installing.
// The raw ota_status and ota_install_progress stay as the truck sent them. No Rivian call is added (the client here counts none beyond the
// handler's own read).
func TestVehicleStateServesTheDerivedUpdateStep(t *testing.T) {
	for _, c := range []struct {
		status   string
		progress float64
		step     string
		percent  int // -1: absent
	}{
		{"Ready_To_Download", 0, "ready_to_download", -1},
		{"Downloading", 35, "downloading", -1},
		{"Preparing", 0, "preparing", -1},
		{"Ready_To_Install", 100, "ready_to_install", -1},
		{"Scheduled_To_Install", 0, "scheduled_to_install", -1},
		{"Install_Countdown", 0, "install_countdown", -1},
		{"Awaiting_Install", 0, "awaiting_install", -1},
		{"Installing", 64, "installing", 64},
		{"Installing", 0, "installing", 0},
		{"Install_Success", 100, "install_success", -1},
		{"Connection_Lost", 0, "connection_lost", -1},
		{"Idle", 0, "", -1},
		{"", 0, "", -1},
		{"Install_Failed", 90, "unknown", -1}, // in Rivian's app, never seen on prod: not named
		{"Something_New", 50, "unknown", -1},
	} {
		client := &restStateClient{st: rivian.State{VehicleID: "veh-1", OtaStatus: c.status, OtaInstallProgress: c.progress}}
		r := chi.NewRouter()
		r.Get("/api/state/{vehicleID}", handleVehicleState(client, nil, nil))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state/veh-1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", c.status, rec.Code)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		var step string
		if v, ok := raw["ota_step"]; ok {
			_ = json.Unmarshal(v, &step)
		} else if c.step != "" {
			t.Errorf("%s: no ota_step key, want %q", c.status, c.step)
		}
		if step != c.step {
			t.Errorf("%s: ota_step %q, want %q", c.status, step, c.step)
		}
		v, ok := raw["ota_install_percent"]
		switch {
		case c.percent < 0 && ok:
			t.Errorf("%s: ota_install_percent %s must be absent unless installing", c.status, v)
		case c.percent >= 0 && (!ok || string(v) != jsonInt(c.percent)):
			t.Errorf("%s: ota_install_percent %s, want %d", c.status, v, c.percent)
		}
		// the raw fields are untouched
		var st rivian.State
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.OtaStatus != c.status || st.OtaInstallProgress != c.progress {
			t.Errorf("%s: raw fields changed: %q %v (%v)", c.status, st.OtaStatus, st.OtaInstallProgress, err)
		}
		if step == "" && strings.Contains(rec.Body.String(), `"ota_step"`) {
			t.Errorf("%s: an absent step must omit the key", c.status)
		}
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// The derived fields are on the response copy only: the state the monitor holds (and publishes, and stores) is never changed.
func TestWithOTADerivedLeavesTheInputAlone(t *testing.T) {
	in := &rivian.State{VehicleID: "v", OtaStatus: "Installing", OtaInstallProgress: 10}
	out := rivian.WithOTADerived(in)
	if in.OtaStep != "" || in.OtaInstallPercent != nil {
		t.Errorf("the input was changed: %+v", in)
	}
	if out == in || out.OtaStep != "installing" || out.OtaInstallPercent == nil || *out.OtaInstallPercent != 10 {
		t.Errorf("out = %+v", out)
	}
	if rivian.WithOTADerived(nil) != nil {
		t.Error("nil must stay nil")
	}
}
