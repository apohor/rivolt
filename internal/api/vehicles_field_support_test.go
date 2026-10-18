package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestLiveFieldSupport(t *testing.T) {
	r2 := rivian.FieldSupport("R2")
	liveFieldSupport(r2, &rivian.State{TimeToEndMins: 90, TimeToEndSource: "estimate"})
	if r2["charge_time_remaining"] != rivian.FieldEstimate {
		t.Errorf("R2 estimate: %q", r2["charge_time_remaining"])
	}
	// A vehicle that reports its own time to full is "reported", whatever the model.
	liveFieldSupport(r2, &rivian.State{TimeToEndMins: 90})
	if r2["charge_time_remaining"] != rivian.FieldReported {
		t.Errorf("vehicle-reported: %q", r2["charge_time_remaining"])
	}
	// Nothing charging: the model's own answer stands.
	r1 := rivian.FieldSupport("R1T")
	liveFieldSupport(r1, &rivian.State{})
	if r1["charge_time_remaining"] != rivian.FieldReported {
		t.Errorf("idle R1: %q", r1["charge_time_remaining"])
	}
}

func TestVehiclesCarryFieldSupport(t *testing.T) {
	c := rivian.NewMock()
	if err := c.Login(t.Context(), rivian.Credentials{Email: "a@b.test", Password: "x"}); err != nil {
		t.Fatalf("mock login: %v", err)
	}
	rec := httptest.NewRecorder()
	handleVehicles(c, nil, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
	var vs []struct {
		Model        string            `json:"model"`
		FieldSupport map[string]string `json:"field_support"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &vs); err != nil || len(vs) == 0 {
		t.Fatalf("%d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	for _, v := range vs {
		if v.FieldSupport["outside_temp"] != "unavailable" || v.FieldSupport["power_state"] == "" {
			t.Errorf("%s: field_support %v", v.Model, v.FieldSupport)
		}
	}
}
