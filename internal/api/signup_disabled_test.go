package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/signuprequests"
)

func signupRoutesStatus(t *testing.T, method, path, body string) int {
	t.Helper()
	h := New(Deps{SignupRequests: signuprequests.New(nil)})
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// RIVOLT_SIGNUP_DISABLED closes the public sign-up surface; with it
// unset the routes are mounted exactly as before.
func TestSignupRoutes_MountedOnlyWhenEnabled(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/signup"},
		{http.MethodPost, "/api/signup/request"},
		{http.MethodGet, "/api/signup/token/abc"},
	}
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "")
	for _, r := range routes {
		if code := signupRoutesStatus(t, r.method, r.path, `{}`); code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			t.Errorf("enabled: %s %s = %d, want the route mounted", r.method, r.path, code)
		}
	}
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "true")
	for _, r := range routes {
		if code := signupRoutesStatus(t, r.method, r.path, `{}`); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("disabled: %s %s = %d, want 404/405", r.method, r.path, code)
		}
	}
}

func TestConfig_SignupEnabledFlag(t *testing.T) {
	get := func() string {
		rec := httptest.NewRecorder()
		handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		return rec.Body.String()
	}
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "")
	if b := get(); !strings.Contains(b, `"signup_enabled":true`) {
		t.Fatalf("default must report signup_enabled true: %s", b)
	}
	t.Setenv("RIVOLT_SIGNUP_DISABLED", "true")
	if b := get(); !strings.Contains(b, `"signup_enabled":false`) {
		t.Fatalf("disabled must report signup_enabled false: %s", b)
	}
}
