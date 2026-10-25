package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/rivian"
)

// A vehicle of an account that has never linked a Rivian session (a seeded vehicle, a user who has not connected yet) used to
// answer 502 on /api/state, which reads as a server failure in the metrics and alerts. It is the user's state: 409 with
// class user_action and reason not_connected. But "no session in this pod" is also what a linked account looks like when its
// stored session could not be restored or read; that stays an error, so nobody is told to reconnect when we simply could not look.
type notLinkedClient struct{ rivian.Client }

func (notLinkedClient) State(context.Context, string) (*rivian.State, error) {
	return nil, rivian.ErrNotAuthenticated
}

type failingStateClient struct {
	rivian.Client
	err error
}

func (c failingStateClient) State(context.Context, string) (*rivian.State, error) { return nil, c.err }

func stateStatus(t *testing.T, path string, h http.HandlerFunc, route string) (int, string) {
	t.Helper()
	r := chi.NewRouter()
	r.Get(route, h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

func TestStateOfAVehicleWithoutALinkedRivianSession(t *testing.T) {
	none := func(context.Context) (bool, error) { return false, nil }
	stored := func(context.Context) (bool, error) { return true, nil }
	unreadable := func(context.Context) (bool, error) { return false, errors.New("decrypt") }
	for _, route := range []struct {
		name string
		h    func(has func(context.Context) (bool, error)) http.HandlerFunc
		path string
		pat  string
	}{
		{"state", func(has func(context.Context) (bool, error)) http.HandlerFunc {
			return handleVehicleState(notLinkedClient{}, nil, has)
		}, "/api/state/veh-1", "/api/state/{vehicleID}"},
		{"fresh", func(has func(context.Context) (bool, error)) http.HandlerFunc {
			return handleVehicleStateFresh(notLinkedClient{}, has)
		}, "/api/state/veh-1/fresh", "/api/state/{vehicleID}/fresh"},
	} {
		// Never linked: 409 with a reason the UI can show, and none of the sentinel's own words.
		code, body := stateStatus(t, route.path, route.h(none), route.pat)
		if code != http.StatusConflict || !strings.Contains(body, `"reason":"not_connected"`) || !strings.Contains(body, `"class":"user_action"`) || strings.Contains(body, "Login") {
			t.Errorf("%s, never linked: %d %s, want 409 not_connected", route.name, code, body)
		}
		// Linked (a session is stored), or unknown whether it is: still an error, not "reconnect".
		for name, has := range map[string]func(context.Context) (bool, error){"stored": stored, "unreadable": unreadable, "unknown": nil} {
			if code, body := stateStatus(t, route.path, route.h(has), route.pat); code != http.StatusBadGateway || strings.Contains(body, "not_connected") {
				t.Errorf("%s, %s session: %d %s, want 502", route.name, name, code, body)
			}
		}
	}
	// Other failures keep their mapping: a dead session is 401, a real upstream failure is 502.
	for err, want := range map[error]int{rivian.ErrNeedsReauth: 401, errors.New("boom"): 502} {
		code, _ := stateStatus(t, "/api/state/veh-1", handleVehicleState(failingStateClient{err: err}, nil, none), "/api/state/{vehicleID}")
		if code != want {
			t.Errorf("%v: %d, want %d", err, code, want)
		}
	}
}
