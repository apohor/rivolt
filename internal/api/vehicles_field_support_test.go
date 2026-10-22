package api

import (
	"context"
	"encoding/json"
	"errors"
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
	handleVehicles(c, nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
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

// A user who has not linked a Rivian account has a client with no session. Their first load of the app asks for the
// vehicle list: that is an empty list, not a 502.
func TestVehiclesOfAnUnlinkedUserIsAnEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	handleVehicles(rivian.NewMock(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("unlinked user: %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

// A client with no session but a stored one (or a stored one that cannot be read) is not "never linked": it stays an
// error, so the user is not told they have no vehicles.
func TestVehiclesOfALinkedUserWithoutALiveSessionIsAnError(t *testing.T) {
	for name, has := range map[string]func(context.Context) (bool, error){
		"stored":     func(context.Context) (bool, error) { return true, nil },
		"unreadable": func(context.Context) (bool, error) { return false, errors.New("decrypt") },
	} {
		rec := httptest.NewRecorder()
		handleVehicles(rivian.NewMock(), nil, nil, nil, has)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: %d %q, want 502", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	handleVehicles(rivian.NewMock(), nil, nil, nil, func(context.Context) (bool, error) { return false, nil })(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("no stored session: %d, want 200", rec.Code)
	}
}

// A linked account whose session died still says so (401 with the reason), and a real upstream failure is still a 502.
func TestVehiclesStillReportRealFailures(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{{rivian.ErrNeedsReauth, http.StatusUnauthorized}, {errors.New("dial tcp: i/o timeout"), http.StatusBadGateway}} {
		rec := httptest.NewRecorder()
		handleVehicles(failingVehicles{err: c.err}, nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
		if rec.Code != c.want {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.want)
		}
	}
}

type failingVehicles struct {
	rivian.Client
	err error
}

func (f failingVehicles) Vehicles(context.Context) ([]rivian.Vehicle, error) { return nil, f.err }
