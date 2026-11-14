package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/rivian"
)

type fakeSim struct {
	run rivian.SimRun
	err error
}

func (f fakeSim) Start(_ context.Context, k rivian.SimKind) (rivian.SimRun, error) {
	f.run.Kind = k
	return f.run, f.err
}
func (f fakeSim) Active(context.Context) (*rivian.SimRun, error) { return nil, f.err }

// The simulated drive and charge routes exist only when the deployment hands the router a SimRunner (main builds one on the preview's admin
// site alone), so prod, the public site and the demo answer 404.
func TestAdminSimRoutesMountedOnlyWhenGiven(t *testing.T) {
	want := []string{"GET /api/admin/sim", "POST /api/admin/sim/drive", "POST /api/admin/sim/charge"}
	mounted := func(d Deps) map[string]bool {
		got := map[string]bool{}
		_ = chi.Walk(newAdminTestRouter(t, d).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(route, "/api/admin/sim") {
				got[method+" "+route] = true
			}
			return nil
		})
		return got
	}
	if got := mounted(Deps{}); len(got) != 0 {
		t.Errorf("sim routes mounted by default: %v", got)
	}
	if got := mounted(Deps{AdminOnly: true}); len(got) != 0 {
		t.Errorf("sim routes mounted on a bare admin site: %v", got)
	}
	got := mounted(Deps{ReplaySim: fakeSim{}})
	for _, k := range want {
		if !got[k] {
			t.Errorf("%s is not mounted with a SimRunner", k)
		}
	}
	// Mounted or not, nobody without a session gets in.
	for _, d := range []Deps{{}, {ReplaySim: fakeSim{}}} {
		rec := httptest.NewRecorder()
		newAdminTestRouter(t, d).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/sim/drive", nil))
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound {
			t.Errorf("anonymous POST /api/admin/sim/drive = %d", rec.Code)
		}
	}
}

func TestAdminSimStartAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"booked":  {nil, http.StatusOK},
		"busy":    {rivian.ErrSimBusy, http.StatusConflict},
		"refused": {&rivian.SimRefusal{Reason: "the sample truck is away from home"}, http.StatusConflict},
		"broken":  {context.DeadlineExceeded, http.StatusInternalServerError},
	} {
		rec := httptest.NewRecorder()
		handleAdminSimStart(fakeSim{err: tc.err}, rivian.SimCharge)(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if rec.Code != tc.code {
			t.Errorf("%s: %d, want %d (%s)", name, rec.Code, tc.code, rec.Body.String())
		}
		if name == "refused" && !strings.Contains(rec.Body.String(), "away from home") {
			t.Errorf("refusal reason missing: %s", rec.Body.String())
		}
		if name == "broken" && strings.Contains(rec.Body.String(), "deadline") {
			t.Errorf("internal error leaked: %s", rec.Body.String())
		}
	}
}
