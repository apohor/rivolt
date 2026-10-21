package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/logging"
)

// asBearer is a request the auth middleware resolved from the iPhone app's
// bearer token.
func asBearer(r *http.Request, uid uuid.UUID) *http.Request {
	return r.WithContext(logging.WithAuthMethod(auth.WithUser(r.Context(), uid), logging.AuthBearer))
}

// The app's token never reaches the admin surface, even an admin's.
func TestAdminGateRefusesBearer(t *testing.T) {
	admin := uuid.New()
	gate := requireAdminMW(func(context.Context, uuid.UUID) (string, error) { return "admin", nil })
	reached := false
	h := gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, asBearer(httptest.NewRequest(http.MethodGet, "/api/admin/users", nil), admin))
	if w.Code != http.StatusForbidden || reached {
		t.Fatalf("admin via bearer: %d reached=%v", w.Code, reached)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/users", nil).WithContext(auth.WithUser(context.Background(), admin)))
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("admin via browser: %d reached=%v", w.Code, reached)
	}
}

// A bearer caller can't view as anyone: the header is ignored, as for a
// non-admin, and the request stays the caller's own.
func TestImpersonationIgnoredForBearer(t *testing.T) {
	admin, target := uuid.New(), uuid.New()
	mw := impersonationMW(func(_ context.Context, uid uuid.UUID) (string, error) {
		if uid == admin {
			return "admin", nil
		}
		return "user", nil
	}, nil)
	var got uuid.UUID
	var swapped bool
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = auth.UserFromContext(r.Context())
		swapped = auth.IsImpersonating(r.Context())
	}))
	r := asBearer(httptest.NewRequest(http.MethodGet, "/api/vehicles", nil), admin)
	r.Header.Set(ImpersonateHeader, target.String())
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got != admin || swapped {
		t.Fatalf("bearer impersonation: user %v swapped %v", got, swapped)
	}
}

// The app's own token can't stand in for a signed-in browser on the sign-in
// pages (it would let one app token mint another).
func TestSignInPagesIgnoreBearer(t *testing.T) {
	uid := uuid.New()
	if _, ok := browserUser(asBearer(httptest.NewRequest(http.MethodGet, appFinishPath, nil), uid)); ok {
		t.Fatal("a bearer caller counted as a signed-in browser")
	}
	r := httptest.NewRequest(http.MethodGet, appFinishPath, nil)
	if got, ok := browserUser(r.WithContext(auth.WithUser(r.Context(), uid))); !ok || got != uid {
		t.Fatal("a browser user wasn't seen")
	}
}
