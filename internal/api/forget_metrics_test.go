package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/sessions"
)

func TestForgetStepsListsEveryFailedStepOnce(t *testing.T) {
	joined := errors.Join(forgetStep("hydra_login", errors.New("a")), forgetStep("kratos_sessions", fmt.Errorf("wrapped: %w", errors.New("b"))), nil, forgetStep("hydra_login", errors.New("c")))
	got := forgetSteps(joined)
	sort.Strings(got)
	if strings.Join(got, ",") != "hydra_login,kratos_sessions" {
		t.Errorf("steps %v", got)
	}
	if got := forgetSteps(errors.New("untagged")); len(got) != 1 || got[0] != "other" {
		t.Errorf("an untagged error is other: %v", got)
	}
	if got := forgetSteps(fmt.Errorf("outer: %w", forgetStep("account_lookup", errors.New("x")))); len(got) != 1 || got[0] != "account_lookup" {
		t.Errorf("a wrapped tag is found: %v", got)
	}
}

// A failed clearing shows up as a counter by entry point and step (and no identity label), so a Hydra or Kratos problem is seen in
// Grafana before anyone notices a browser that would not stay signed out.
func TestForgetFailuresAreCountedByEntryAndStep(t *testing.T) {
	m := metrics.New()
	f := &fakeIdpAdmin{ident: "kid-1", failing: "/sessions/login"} // Hydra's login-session delete answers 500
	rev := newRevoker(t, f)
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	failures := func(entry, step string) float64 {
		return testutil.ToFloat64(m.LogoutForgetFailuresTotal.WithLabelValues(entry, step))
	}
	attempts := func(entry string) float64 {
		return testutil.ToFloat64(m.LogoutForgetAttemptsTotal.WithLabelValues(entry))
	}

	// Plain sign-out: Hydra's login delete fails, the sign-out still happens, the failure is counted.
	uid := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uid))
	rec := httptest.NewRecorder()
	Deps{Auth: svc, LoginForgetter: rev, Metrics: m}.logoutHandler()(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if attempts("logout") != 1 || failures("logout", "hydra_login") != 1 || failures("logout", "kratos_sessions") != 0 {
		t.Errorf("logout: attempts %v, hydra_login %v, kratos_sessions %v", attempts("logout"), failures("logout", "hydra_login"), failures("logout", "kratos_sessions"))
	}

	// Revoke one session and sign out others carry their own entry labels.
	me, cur, other := uuid.New(), uuid.New(), uuid.New()
	fs := &fakeSessions{owner: me, rows: []sessions.Session{{ID: cur, UserID: me, Kind: sessions.KindWeb}, {ID: other, UserID: me, Kind: sessions.KindWeb}}}
	a := &SessionsAPI{Store: fs, Current: func(*http.Request) (uuid.UUID, bool) { return cur, true }, Remembered: rev, Observe: NewForgetObserver(m)}
	sessReq(a.Revoke, "DELETE", "/api/auth/sessions/"+other.String(), me, false)
	sessReq(a.RevokeOthers, "POST", "/api/auth/sessions/revoke-others", me, false)
	if attempts("sign_out_one") != 1 || failures("sign_out_one", "hydra_login") != 1 || attempts("sign_out_others") != 1 || failures("sign_out_others", "hydra_login") != 1 {
		t.Errorf("sessions entries not counted: one %v/%v, others %v/%v", attempts("sign_out_one"), failures("sign_out_one", "hydra_login"), attempts("sign_out_others"), failures("sign_out_others", "hydra_login"))
	}

	// A clearing that works counts an attempt and no failure.
	f.failing = ""
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uid))
	Deps{Auth: svc, LoginForgetter: rev, Metrics: m}.logoutHandler()(rec, req)
	if attempts("logout") != 2 || failures("logout", "hydra_login") != 1 {
		t.Errorf("a success is an attempt and no failure: attempts %v, failures %v", attempts("logout"), failures("logout", "hydra_login"))
	}

	// No identity label anywhere: the only label names are entry and step.
	mfs, _ := m.Registry().Gather()
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "rivolt_logout_forget_") {
			continue
		}
		for _, mm := range mf.GetMetric() {
			for _, l := range mm.GetLabel() {
				if l.GetName() != "entry" && l.GetName() != "step" {
					t.Errorf("%s has a label %q: only entry and step are allowed", mf.GetName(), l.GetName())
				}
			}
		}
	}

	// And nothing panics without metrics.
	var none ForgetObserver
	none.observe("logout", errors.New("x"))
	if NewForgetObserver(nil) != nil {
		t.Error("no metrics gives no observer")
	}
}

// The password routes and support's secure-account count under their own entry labels.
func TestForgetIsCountedForThePasswordAndAdminRoutes(t *testing.T) {
	m := metrics.New()
	obs := NewForgetObserver(m)
	failures := func(entry, step string) float64 {
		return testutil.ToFloat64(m.LogoutForgetFailuresTotal.WithLabelValues(entry, step))
	}
	attempts := func(entry string) float64 {
		return testutil.ToFloat64(m.LogoutForgetAttemptsTotal.WithLabelValues(entry))
	}

	e := newSecEnv(t)
	e.api.Observe = obs
	e.rev.failFirst = 1 // the first clearing fails
	e.post(e.api.change, `{"current":"old-pw","new":"a-better-pw-9"}`, nil)
	e.post(e.api.secure, `{"current":"old-pw","new":"another-pw-99"}`, nil)
	if attempts("password") != 1 || failures("password", "other") != 1 || attempts("secure") != 1 || failures("secure", "other") != 0 {
		t.Errorf("password %v/%v, secure %v/%v", attempts("password"), failures("password", "other"), attempts("secure"), failures("secure", "other"))
	}

	s := newSecureEnv(t)
	s.a.Observe = obs
	s.rev.failFirst = 1
	s.post(uuid.NewString())
	if attempts("admin_secure") != 1 || failures("admin_secure", "other") != 1 {
		t.Errorf("admin_secure %v/%v", attempts("admin_secure"), failures("admin_secure", "other"))
	}
}
