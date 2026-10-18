package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// withUserVehicles makes the user's vehicle list the given ids for one test.
func withUserVehicles(t *testing.T, ids ...string) {
	t.Helper()
	old := userVehicleIDs
	userVehicleIDs = func(context.Context, *sql.DB, uuid.UUID) ([]string, error) { return ids, nil }
	t.Cleanup(func() { userVehicleIDs = old })
}

func TestOwnsVehicle(t *testing.T) {
	ctx, uid, pool := context.Background(), uuid.New(), &sql.DB{}
	withUserVehicles(t, "mine")
	if !ownsVehicle(ctx, nil, pool, uid, "mine") {
		t.Error("own vehicle refused")
	}
	if ownsVehicle(ctx, nil, pool, uid, "theirs") {
		t.Error("foreign vehicle accepted")
	}
	if !ownsVehicle(ctx, nil, nil, uid, "anything") {
		t.Error("no monitor and no database has no other user to protect")
	}
	if ownsVehicle(ctx, (*rivian.StateMonitor)(nil), nil, uid, "x") != true {
		t.Error("nothing to check against should allow")
	}
	userVehicleIDs = func(context.Context, *sql.DB, uuid.UUID) ([]string, error) { return nil, sql.ErrConnDone }
	if ownsVehicle(ctx, nil, pool, uid, "mine") {
		t.Error("a failed lookup must refuse, not pass")
	}
}

func TestForeignVehicleIs400OnEveryPlannerRoute(t *testing.T) {
	withUserVehicles(t, "v1")
	pool := &sql.DB{}
	cp := &capturePlanner{}
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: cp}
	single := strings.Replace(planWith(""), `"vehicle_id":"v1"`, `"vehicle_id":"someone-elses"`, 1)
	multi := `{"vehicle_id":"someone-elses","starting_soc":90,"pack_kwh":131,
	  "start_at":"2026-10-20T08:00:00-07:00","tz":"America/Los_Angeles",
	  "origin":{"lat":34.16,"lon":-118.17},"days":[{"end":{"lat":36.0,"lon":-119.96}}]}`
	for name, tc := range map[string]struct {
		h    http.HandlerFunc
		body string
	}{
		"/trips/plan":          {handleTripPlan(c, nil, pool, uuid.New(), nil, nil, nil, nil), single},
		"/trips/plan-multiday": {handleTripPlanMultiday(c, nil, pool, uuid.New(), nil, nil, nil, nil), multi},
	} {
		rec := httptest.NewRecorder()
		tc.h(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(tc.body)))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unknown vehicle") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if cp.got.Waypoints != nil {
		t.Error("a planner was called for a foreign vehicle")
	}
}
