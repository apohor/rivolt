package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// roleMap is a roleLookup stub: a fixed uid→role table. An unknown uid
// returns "" (mirrors db.RoleFor, where role is NOT NULL so an empty
// string can only mean "no such user").
type roleMap map[uuid.UUID]string

func (m roleMap) lookup(_ context.Context, uid uuid.UUID) (string, error) {
	return m[uid], nil
}

// ctxCapture records what the next handler saw so tests can assert on
// the swapped identity.
type ctxCapture struct {
	called       bool
	uid          uuid.UUID
	impersonator uuid.UUID
	impersonated bool
}

// serveImpersonation runs a request through impersonationMW with the
// real caller already resolved into context (as the auth middleware
// would have done), and returns the recorder plus what next saw.
func serveImpersonation(t *testing.T, roles roleMap, enabled bool, caller uuid.UUID, method, header string) (*httptest.ResponseRecorder, *ctxCapture) {
	t.Helper()
	cap := &ctxCapture{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.called = true
		cap.uid, _ = auth.UserFromContext(r.Context())
		cap.impersonator, cap.impersonated = auth.ImpersonatorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := impersonationMW(roles.lookup, enabled)(next)

	req := httptest.NewRequest(method, "/api/drives", nil)
	if caller != uuid.Nil {
		req = req.WithContext(auth.WithUser(req.Context(), caller))
	}
	if header != "" {
		req.Header.Set(impersonationHeader, header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w, cap
}

func TestImpersonation_AdminSwapsToTarget(t *testing.T) {
	admin, target := uuid.New(), uuid.New()
	roles := roleMap{admin: "admin", target: "user"}

	w, cap := serveImpersonation(t, roles, true, admin, http.MethodGet, target.String())

	if w.Code != http.StatusOK || !cap.called {
		t.Fatalf("want passthrough 200, got %d called=%v", w.Code, cap.called)
	}
	if cap.uid != target {
		t.Errorf("downstream uid = %s, want target %s", cap.uid, target)
	}
	if !cap.impersonated || cap.impersonator != admin {
		t.Errorf("impersonator = %s (set=%v), want admin %s", cap.impersonator, cap.impersonated, admin)
	}
}

func TestImpersonation_NonAdminHeaderIgnored(t *testing.T) {
	caller, target := uuid.New(), uuid.New()
	roles := roleMap{caller: "user", target: "user"}

	w, cap := serveImpersonation(t, roles, true, caller, http.MethodGet, target.String())

	if w.Code != http.StatusOK || !cap.called {
		t.Fatalf("want passthrough 200, got %d called=%v", w.Code, cap.called)
	}
	if cap.uid != caller {
		t.Errorf("downstream uid = %s, want caller %s (header must be ignored)", cap.uid, caller)
	}
	if cap.impersonated {
		t.Error("non-admin request must not be marked impersonated")
	}
}

func TestImpersonation_NonGETIsReadOnly405(t *testing.T) {
	admin, target := uuid.New(), uuid.New()
	roles := roleMap{admin: "admin", target: "user"}

	w, cap := serveImpersonation(t, roles, true, admin, http.MethodPost, target.String())

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("impersonated POST: want 405, got %d", w.Code)
	}
	if cap.called {
		t.Error("next handler must not run for a rejected write")
	}
}

func TestImpersonation_CannotImpersonateAdmin(t *testing.T) {
	admin, other := uuid.New(), uuid.New()
	roles := roleMap{admin: "admin", other: "admin"}

	w, cap := serveImpersonation(t, roles, true, admin, http.MethodGet, other.String())

	if w.Code != http.StatusForbidden {
		t.Fatalf("impersonating an admin: want 403, got %d", w.Code)
	}
	if cap.called {
		t.Error("next handler must not run when refused")
	}
}

func TestImpersonation_SelfImpersonationForbidden(t *testing.T) {
	admin := uuid.New()
	roles := roleMap{admin: "admin"}

	// Target == caller, who is an admin, so this is refused as an
	// admin target (403) rather than silently swapping to self.
	w, _ := serveImpersonation(t, roles, true, admin, http.MethodGet, admin.String())
	if w.Code != http.StatusForbidden {
		t.Fatalf("self-impersonation: want 403, got %d", w.Code)
	}
}

func TestImpersonation_DisabledIgnoresHeader(t *testing.T) {
	admin, target := uuid.New(), uuid.New()
	roles := roleMap{admin: "admin", target: "user"}

	w, cap := serveImpersonation(t, roles, false, admin, http.MethodGet, target.String())

	if w.Code != http.StatusOK || !cap.called {
		t.Fatalf("disabled: want passthrough 200, got %d", w.Code)
	}
	if cap.uid != admin || cap.impersonated {
		t.Errorf("disabled feature must ignore header: uid=%s impersonated=%v", cap.uid, cap.impersonated)
	}
}

func TestImpersonation_InvalidTarget400(t *testing.T) {
	admin := uuid.New()
	roles := roleMap{admin: "admin"}

	w, cap := serveImpersonation(t, roles, true, admin, http.MethodGet, "not-a-uuid")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed target: want 400, got %d", w.Code)
	}
	if cap.called {
		t.Error("next must not run on a bad target")
	}
}

func TestImpersonation_TargetNotFound404(t *testing.T) {
	admin := uuid.New()
	roles := roleMap{admin: "admin"} // target uid absent → role ""

	w, _ := serveImpersonation(t, roles, true, admin, http.MethodGet, uuid.New().String())
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown target: want 404, got %d", w.Code)
	}
}

func TestImpersonation_NoHeaderPassesThrough(t *testing.T) {
	admin := uuid.New()
	roles := roleMap{admin: "admin"}

	w, cap := serveImpersonation(t, roles, true, admin, http.MethodGet, "")
	if w.Code != http.StatusOK || !cap.called || cap.uid != admin || cap.impersonated {
		t.Fatalf("no header: want plain passthrough as caller, got %d uid=%s imp=%v", w.Code, cap.uid, cap.impersonated)
	}
}

// requireAdminMW must refuse any impersonated request outright, so an
// impersonated context can never reach an admin handler (no privilege
// chaining) even for a GET.
func TestRequireAdmin_RefusesImpersonatedContext(t *testing.T) {
	admin, target := uuid.New(), uuid.New()
	roles := roleMap{admin: "admin", target: "user"}

	next := &testHandler{}
	h := requireAdminMW(roles.lookup)(next)

	// Impersonated context: primary identity is the (non-admin) target,
	// impersonator is the admin — exactly what impersonationMW builds.
	ctx := auth.WithImpersonator(context.Background(), admin)
	ctx = auth.WithUser(ctx, target)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("impersonated admin-route request: want 403, got %d", w.Code)
	}
	if next.called {
		t.Error("admin handler must not run under impersonation")
	}
}

func TestRequireAdmin_AllowsRealAdmin(t *testing.T) {
	admin := uuid.New()
	roles := roleMap{admin: "admin"}

	next := &testHandler{}
	h := requireAdminMW(roles.lookup)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil).
		WithContext(auth.WithUser(context.Background(), admin))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK || !next.called {
		t.Fatalf("real admin: want 200 passthrough, got %d called=%v", w.Code, next.called)
	}
}
