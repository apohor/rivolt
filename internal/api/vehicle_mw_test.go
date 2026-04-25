package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// testHandler is a no-op next-handler that records whether it was
// reached. The middleware contract is: owned → pass through; not
// owned → 404 without touching next; error → 500 without touching
// next.
type testHandler struct{ called bool }

func (t *testHandler) ServeHTTP(http.ResponseWriter, *http.Request) { t.called = true }

// mountVehicleRoute wires a single /v/{vehicleID} route through the
// ownership middleware and a recording handler. Returns the mux +
// the next-called flag so each test can assert both pass-through
// and status separately.
func mountVehicleRoute(check vehicleOwnershipCheck) (*chi.Mux, *testHandler) {
	h := &testHandler{}
	r := chi.NewRouter()
	r.With(vehicleOwnershipMW(check, slog.New(slog.NewTextHandler(&nullWriter{}, nil)))).
		Get("/v/{vehicleID}", h.ServeHTTP)
	return r, h
}

type nullWriter struct{}

func (*nullWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestVehicleOwnershipMW_OwnedPassesThrough(t *testing.T) {
	uid := uuid.New()
	mux, next := mountVehicleRoute(func(_ context.Context, userID uuid.UUID, rivianID string) (bool, error) {
		if userID != uid {
			t.Errorf("check received wrong user: %s", userID)
		}
		if rivianID != "01-123" {
			t.Errorf("check received wrong vehicle: %s", rivianID)
		}
		return true, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/v/01-123", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uid))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("owned request: want 200, got %d", w.Code)
	}
	if !next.called {
		t.Fatalf("owned request: next handler was not reached")
	}
}

// TestVehicleOwnershipMW_NotOwnedReturns404 covers the core
// tenant-enumeration-oracle closure. A 403 here would leak that
// the vehicle-id exists on the server but belongs to someone
// else; 404 collapses "not yours" and "doesn't exist" into the
// same answer.
func TestVehicleOwnershipMW_NotOwnedReturns404(t *testing.T) {
	mux, next := mountVehicleRoute(func(context.Context, uuid.UUID, string) (bool, error) {
		return false, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/v/01-stranger", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unowned request: want 404, got %d", w.Code)
	}
	if next.called {
		t.Fatalf("unowned request: next handler should not have run")
	}
}

// TestVehicleOwnershipMW_CheckErrorReturns500 ensures a Postgres
// outage doesn't silently 404 every request (which would look
// identical to "nothing exists" to callers and mask the incident).
func TestVehicleOwnershipMW_CheckErrorReturns500(t *testing.T) {
	mux, next := mountVehicleRoute(func(context.Context, uuid.UUID, string) (bool, error) {
		return false, errors.New("db down")
	})

	req := httptest.NewRequest(http.MethodGet, "/v/01-123", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("db error: want 500, got %d", w.Code)
	}
	if next.called {
		t.Fatalf("db error: next handler should not have run")
	}
}

// TestVehicleOwnershipMW_NoUserFallsOpen covers the legacy
// single-tenant deployment: RIVOLT_USERNAME unset, the auth chain
// is a no-op, and the stores are bound to a static operator
// identity. The middleware must not 404 in this mode or the whole
// self-hosted UX breaks.
func TestVehicleOwnershipMW_NoUserFallsOpen(t *testing.T) {
	checkCalled := false
	mux, next := mountVehicleRoute(func(context.Context, uuid.UUID, string) (bool, error) {
		checkCalled = true
		return false, nil
	})

	// No auth.WithUser on the context → UserFromContext returns !ok.
	req := httptest.NewRequest(http.MethodGet, "/v/01-123", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("auth-disabled request: want 200, got %d", w.Code)
	}
	if !next.called {
		t.Fatalf("auth-disabled request: next handler was not reached")
	}
	if checkCalled {
		t.Fatalf("auth-disabled request: DB check should be skipped, not run")
	}
}
