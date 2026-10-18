package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

func newMeAuth(t *testing.T) *auth.Service {
	t.Helper()
	s, err := auth.New(auth.Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    func(string) uuid.UUID { return uuid.New() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func meGet(t *testing.T, svc *auth.Service, mutate func(*http.Request) *http.Request) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	if mutate != nil {
		req = mutate(req)
	}
	rec := httptest.NewRecorder()
	handleMeEnriched(svc, nil, false)(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// A signed-out visitor with no credential is not an error.
func TestAuthMe_SignedOutIs200Unauthenticated(t *testing.T) {
	code, body := meGet(t, newMeAuth(t), nil)
	if code != http.StatusOK || body["authenticated"] != false {
		t.Fatalf("code %d body %v, want 200 {authenticated:false}", code, body)
	}
	if _, has := body["user_id"]; has {
		t.Fatalf("signed-out body must not carry identity: %v", body)
	}
}

// An expired or invalid session (a credential was presented but did
// not resolve) keeps the 401 the SPA relies on.
func TestAuthMe_PresentedButUnresolvedCredentialStays401(t *testing.T) {
	for _, name := range []string{"rivolt_session", "ory_kratos_session"} {
		code, _ := meGet(t, newMeAuth(t), func(r *http.Request) *http.Request {
			r.AddCookie(&http.Cookie{Name: name, Value: "stale"})
			return r
		})
		if code != http.StatusUnauthorized {
			t.Errorf("cookie %s unresolved: code %d, want 401", name, code)
		}
	}
}

func TestAuthMe_SignedInIsUnchangedPlusAuthenticated(t *testing.T) {
	uid := uuid.New()
	code, body := meGet(t, newMeAuth(t), func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUser(r.Context(), uid))
	})
	if code != http.StatusOK || body["authenticated"] != true || body["user_id"] != uid.String() || body["role"] != "user" {
		t.Fatalf("code %d body %v", code, body)
	}
	if _, ok := body["onboarding_completed"]; !ok {
		t.Fatalf("existing fields must remain: %v", body)
	}
}

// The signed-out 200 must not be cacheable.
func TestAuthMe_SignedOutResponseIsNoStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	handleMeEnriched(newMeAuth(t), nil, false)(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code %d Cache-Control %q, want 200 no-store", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
