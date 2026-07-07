package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// roleLookupStub builds a roleLookup closure over a fixed uid→role
// map, standing in for db.RoleFor. Unlisted uids resolve to "" (not
// admin), matching the zero value a fresh users row would have.
func roleLookupStub(roles map[uuid.UUID]string) func(context.Context, uuid.UUID) (string, error) {
	return func(_ context.Context, uid uuid.UUID) (string, error) {
		return roles[uid], nil
	}
}

// alwaysEnabled / never toggles the RIVOLT_IMPERSONATION_DISABLED
// gate for tests that don't care about it.
func alwaysEnabled() bool { return false }

// TestImpersonationMW_AdminWithHeaderSwapsContext is the core
// contract: an admin caller with a valid target header gets the
// request context's identity swapped to the target, and the real
// caller is recoverable via auth.ImpersonatorFromContext for audit.
func TestImpersonationMW_AdminWithHeaderSwapsContext(t *testing.T) {
	admin := uuid.New()
	target := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", target: "user"})

	var sawUID, sawImpersonator uuid.UUID
	var sawImpersonating bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUID, _ = auth.UserFromContext(r.Context())
		sawImpersonator, sawImpersonating = auth.ImpersonatorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
	req.Header.Set(ImpersonateHeader, target.String())
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	w := httptest.NewRecorder()
	impersonationMW(roles, alwaysEnabled)(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if sawUID != target {
		t.Errorf("context uid = %s, want target %s", sawUID, target)
	}
	if !sawImpersonating || sawImpersonator != admin {
		t.Errorf("impersonator context = %s, ok=%v; want %s, true", sawImpersonator, sawImpersonating, admin)
	}
}

// TestImpersonationMW_NonAdminHeaderIgnored is the "never trust the
// header from a non-admin" guarantee — a regular user sending the
// header must see their own context untouched, with no error either
// (so probing the mechanism's existence tells them nothing).
func TestImpersonationMW_NonAdminHeaderIgnored(t *testing.T) {
	caller := uuid.New()
	target := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{caller: "user", target: "user"})

	var sawUID uuid.UUID
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUID, _ = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
	req.Header.Set(ImpersonateHeader, target.String())
	req = req.WithContext(auth.WithUser(req.Context(), caller))
	w := httptest.NewRecorder()
	impersonationMW(roles, alwaysEnabled)(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if sawUID != caller {
		t.Errorf("context uid = %s, want unchanged caller %s", sawUID, caller)
	}
}

// TestImpersonationMW_NonGETReturns405 covers the read-only
// guardrail: any method other than GET/HEAD is refused while a
// valid impersonation header is present, and next never runs.
func TestImpersonationMW_NonGETReturns405(t *testing.T) {
	admin := uuid.New()
	target := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", target: "user"})

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/charges/clusters", nil)
	req.Header.Set(ImpersonateHeader, target.String())
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	w := httptest.NewRecorder()
	impersonationMW(roles, alwaysEnabled)(next).ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if called {
		t.Error("next ran on a non-GET request while impersonating")
	}
}

// TestImpersonationMW_AdminRouteRefused is the no-privilege-chaining
// guardrail: /api/admin/* is refused outright while impersonating,
// before any identity swap, regardless of method.
func TestImpersonationMW_AdminRouteRefused(t *testing.T) {
	admin := uuid.New()
	target := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", target: "user"})

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set(ImpersonateHeader, target.String())
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	w := httptest.NewRecorder()
	impersonationMW(roles, alwaysEnabled)(next).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if called {
		t.Error("next ran on /api/admin/* while impersonating")
	}
}

// TestImpersonationMW_CannotImpersonateAdmin caps the blast radius
// of a compromised admin session: an admin target is always refused,
// even for another confirmed admin caller.
func TestImpersonationMW_CannotImpersonateAdmin(t *testing.T) {
	admin := uuid.New()
	otherAdmin := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", otherAdmin: "admin"})

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
	req.Header.Set(ImpersonateHeader, otherAdmin.String())
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	w := httptest.NewRecorder()
	impersonationMW(roles, alwaysEnabled)(next).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if called {
		t.Error("next ran while impersonating an admin target")
	}
}

// TestImpersonationMW_DisabledFlagIgnoresHeader covers
// RIVOLT_IMPERSONATION_DISABLED: even a valid admin+non-admin-target
// header must be a complete no-op when the operator flag is set.
func TestImpersonationMW_DisabledFlagIgnoresHeader(t *testing.T) {
	admin := uuid.New()
	target := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", target: "user"})

	var sawUID uuid.UUID
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUID, _ = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
	req.Header.Set(ImpersonateHeader, target.String())
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	w := httptest.NewRecorder()
	impersonationMW(roles, func() bool { return true })(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if sawUID != admin {
		t.Errorf("context uid = %s, want unchanged admin %s", sawUID, admin)
	}
}

// TestImpersonationMW_NoHeaderPassesThrough sanity-checks the
// overwhelmingly common case (no impersonation in play at all)
// stays a complete no-op.
func TestImpersonationMW_NoHeaderPassesThrough(t *testing.T) {
	uid := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{uid: "admin"})

	var sawUID uuid.UUID
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUID, _ = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uid))
	w := httptest.NewRecorder()
	impersonationMW(roles, alwaysEnabled)(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK || sawUID != uid {
		t.Fatalf("status=%d uid=%s, want 200/%s", w.Code, sawUID, uid)
	}
}

// TestRequireAdminMW_RefusesImpersonatedContext is the belt-and-
// braces layer inside requireAdminMW itself: even if a context were
// somehow both impersonated and role-lookup=admin (shouldn't
// happen — impersonation targets can never be admins — but this is
// defense in depth, not reliance on that invariant), the admin gate
// still refuses it.
func TestRequireAdminMW_RefusesImpersonatedContext(t *testing.T) {
	admin := uuid.New()
	target := uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", target: "admin"})

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	ctx := auth.WithImpersonation(context.Background(), admin, target)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	requireAdminMW(roles)(next).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if called {
		t.Error("next ran for an impersonated context inside requireAdminMW")
	}
}
