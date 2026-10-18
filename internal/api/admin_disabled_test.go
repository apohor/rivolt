package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

func newAdminTestRouter(t *testing.T, d Deps) http.Handler {
	t.Helper()
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	d.Auth, d.AuthEnforced = svc, true
	return New(d)
}

// RIVOLT_ADMIN_DISABLED: no admin surface on this deployment. The admin
// page and API are 404 before auth (so not even an admin session reaches
// them, and the answer doesn't confirm they exist); the rest is untouched.
func TestAdminDisabled404s(t *testing.T) {
	get := func(h http.Handler, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	off := newAdminTestRouter(t, Deps{AdminDisabled: true})
	for _, p := range []string{"/admin", "/admin/", "/admin/users", "/api/admin", "/api/admin/users", "/api/admin/kill-switch"} {
		if code := get(off, p); code != http.StatusNotFound {
			t.Errorf("disabled: GET %s = %d, want 404", p, code)
		}
	}
	if code := get(off, "/api/health"); code == http.StatusNotFound {
		t.Errorf("disabled: /api/health is 404")
	}
	on := newAdminTestRouter(t, Deps{})
	if code := get(on, "/api/admin/users"); code == http.StatusNotFound {
		t.Errorf("enabled: /api/admin/users is 404 (want the auth gate's 401)")
	}
}

func TestIsAdminPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/admin": true, "/admin/": true, "/admin/x": true, "/api/admin": true, "/api/admin/users/1": true,
		"/administrator": false, "/api/admins": false, "/api/auth/me": false, "/": false, "/adminx/admin": false,
	} {
		if got := isAdminPath(p); got != want {
			t.Errorf("isAdminPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// The raw Rivian probes are only mounted with RIVOLT_ADMIN_DEBUG_PROBES.
func TestAdminDebugProbesMountedOnlyWhenOn(t *testing.T) {
	probes := map[string]bool{
		"POST /api/admin/trips/plan/raw": true, "GET /api/admin/trips/plan/diag": true,
		"GET /api/admin/gql/type": true, "GET /api/admin/gql/enum": true, "POST /api/admin/gql/raw": true,
	}
	mounted := func(d Deps) map[string]bool {
		got := map[string]bool{}
		_ = chi.Walk(newAdminTestRouter(t, d).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if k := method + " " + route; probes[k] {
				got[k] = true
			}
			return nil
		})
		return got
	}
	if got := mounted(Deps{}); len(got) != 0 {
		t.Errorf("probes mounted by default: %v", got)
	}
	if got := mounted(Deps{AdminDebugProbes: true}); len(got) != len(probes) {
		t.Errorf("with the flag: %d of %d probes mounted: %v", len(got), len(probes), got)
	}
	// The rest of the admin API stays mounted without the flag.
	found := false
	_ = chi.Walk(newAdminTestRouter(t, Deps{}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found = found || (method == http.MethodGet && route == "/api/admin/users")
		return nil
	})
	if !found {
		t.Error("GET /api/admin/users isn't mounted without the probes flag")
	}
}

func TestMeRoleHidesAdmin(t *testing.T) {
	for _, c := range []struct {
		role string
		hide bool
		want string
	}{{"admin", false, "admin"}, {"admin", true, "user"}, {"user", true, "user"}, {"", false, "user"}} {
		if got := meRole(c.role, c.hide); got != c.want {
			t.Errorf("meRole(%q, %v) = %q, want %q", c.role, c.hide, got, c.want)
		}
	}
}

// With the admin surface off, impersonation is off too, and /api/config
// tells the SPA so (it hides the "View as" control).
func TestAdminDisabledTurnsImpersonationOff(t *testing.T) {
	disabled := func(d Deps) bool {
		rec := httptest.NewRecorder()
		newAdminTestRouter(t, d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var cfg struct {
			Impersonation struct {
				Disabled bool `json:"disabled"`
			} `json:"impersonation"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("config: %v: %s", err, rec.Body.String())
		}
		return cfg.Impersonation.Disabled
	}
	if disabled(Deps{}) {
		t.Error("impersonation off by default")
	}
	if !disabled(Deps{AdminDisabled: true}) {
		t.Error("impersonation still on with the admin surface disabled")
	}
}
