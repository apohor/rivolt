package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

const demoVeh = "demo-0000"

func TestDemoAllowed(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/vehicles", true},
		{"HEAD", "/api/charges", true},
		{"GET", "/api/charges/", true},
		{"GET", "/api/ledger/monthly", true},
		{"GET", "/api/trips", true},
		{"GET", "/api/state/" + demoVeh, true},
		{"GET", "/api/vehicles/" + demoVeh + "/now", true},
		// other vehicles
		{"GET", "/api/state/other", false},
		{"GET", "/api/vehicles/other/now", false},
		{"GET", "/api/state/" + demoVeh + "/debug", false},
		{"GET", "/api/state/" + demoVeh + "/fresh", false},
		// writes and non-allowlisted reads
		{"POST", "/api/charges", false},
		{"PATCH", "/api/charges/x/pricing", false},
		{"DELETE", "/api/charges/x", false},
		{"PUT", "/api/settings/charging", false},
		{"GET", "/api/settings/charging", false},
		{"GET", "/api/charges/clusters", false},
		{"GET", "/api/admin/users", false},
		{"GET", "/api/auth/me", false},
		{"GET", "/api/vehicles/owned", false},
		{"GET", "/api/ledger/export", false},
		{"GET", "/api/ledger/../admin/users", false},
	}
	for _, c := range cases {
		if got := demoAllowed(c.method, c.path, demoVeh); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func demoChain(limiter *ipLimiter, real *uuid.UUID) (http.Handler, uuid.UUID) {
	demoUID := uuid.New()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := auth.UserFromContext(r.Context()); ok {
			w.Header().Set("X-Test-User", u.String())
		}
		w.WriteHeader(http.StatusOK)
	})
	h := demoAccessMW(demoUID, demoVeh, limiter)(inner)
	if real != nil {
		// Simulate a session resolved by the auth middleware upstream.
		h = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), *real)))
			})
		}(h)
	}
	return h, demoUID
}

func demoReq(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	h.ServeHTTP(rec, req)
	return rec
}

func TestDemoAccessMW_AnonymousAllowlistedBecomesDemoUser(t *testing.T) {
	h, uid := demoChain(newDemoLimiter(), nil)
	rec := demoReq(h, "GET", "/api/charges")
	if rec.Header().Get("X-Test-User") != uid.String() || rec.Header().Get(demoHeader) != "1" {
		t.Fatalf("anonymous allowlisted GET not served as demo: %v", rec.Header())
	}
	for _, c := range [][2]string{
		{"POST", "/api/charges"},
		{"GET", "/api/admin/users"},
		{"GET", "/api/state/other"},
		{"GET", "/api/settings/charging"},
	} {
		rec := demoReq(h, c[0], c[1])
		if rec.Header().Get("X-Test-User") != "" || rec.Header().Get(demoHeader) != "" {
			t.Errorf("%s %s got demo identity", c[0], c[1])
		}
	}
}

func TestDemoAccessMW_SignedInUserIsNeverSwitched(t *testing.T) {
	real := uuid.New()
	h, _ := demoChain(newDemoLimiter(), &real)
	rec := demoReq(h, "GET", "/api/charges")
	if rec.Header().Get("X-Test-User") != real.String() || rec.Header().Get(demoHeader) != "" {
		t.Fatalf("signed-in user was switched: %v", rec.Header())
	}
}

func TestDemoAccessMW_RateLimited(t *testing.T) {
	h, _ := demoChain(newIPLimiter(3, time.Minute, time.Hour), nil)
	codes := []int{}
	for i := 0; i < 5; i++ {
		codes = append(codes, demoReq(h, "GET", "/api/charges").Code)
	}
	if codes[2] != 200 || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
	// Non-allowlisted requests don't consume demo budget.
	if demoReq(h, "GET", "/api/admin/users").Code != 200 {
		t.Fatal("non-demo request was rate limited by the demo limiter")
	}
}

func TestDemoConfigFlagOffByDefault(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "")
	t.Setenv("RIVIAN_CLIENT", "replay")
	rec := httptest.NewRecorder()
	handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest("GET", "/api/config", nil))
	if !contains(rec.Body.String(), `"demo_enabled":false`) {
		t.Fatalf("demo_enabled should be false: %s", rec.Body)
	}
	t.Setenv("RIVOLT_DEMO", "true")
	rec = httptest.NewRecorder()
	handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest("GET", "/api/config", nil))
	if !contains(rec.Body.String(), `"demo_enabled":true`) {
		t.Fatalf("demo_enabled should be true: %s", rec.Body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
