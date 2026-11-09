package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

// The label is bounded: a plain integer is itself (up to a limit of distinct ones), a missing header is "none", anything else "unknown".
func TestAppBuildLabel(t *testing.T) {
	c := NewAppBuildCounter()
	for in, want := range map[string]string{
		"": "none", "  ": "none", "431": "431", " 431 ": "431", "abc": "unknown", "4.3": "unknown", "-1": "unknown", "0431": "unknown",
		"431 or else": "unknown", "999999999999": "unknown",
	} {
		if got := c.label(in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
	}
	for i := 1; i <= appBuildLabels+10; i++ {
		c.label(strconv.Itoa(i))
	}
	if got := c.label("77777"); got != "other" {
		t.Errorf("a build past the bound = %q, want other", got)
	}
	if got := c.label("431"); got != "431" {
		t.Errorf("a build already seen kept its label: %q", got)
	}
}

// Only app sessions are counted, by the build they send; the web (a cookie session) and signed-out requests are not.
func TestAppBuildMiddlewareCountsOnlyAppSessions(t *testing.T) {
	c := NewAppBuildCounter()
	h := appBuildMW(c)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	do := func(kind string, signedIn bool, build string) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		if build != "" {
			req.Header.Set(appBuildHeader, build)
		}
		if signedIn {
			req = req.WithContext(auth.WithSession(req.Context(), auth.SessionRef{ID: uuid.New(), Kind: kind}))
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	do(sessions.KindApp, true, "431")
	do(sessions.KindApp, true, "431")
	do(sessions.KindApp, true, "")     // an app build that does not send the header
	do(sessions.KindApp, true, "junk") // malformed
	do(sessions.KindWeb, true, "431")  // a browser: not an app session
	do("", false, "431")               // signed out
	for build, want := range map[string]float64{"431": 2, "none": 1, "unknown": 1} {
		if got := testutil.ToFloat64(c.vec.WithLabelValues(build)); got != want {
			t.Errorf("build %q counted %v, want %v", build, got, want)
		}
	}
}
