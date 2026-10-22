package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// Through the real router with full URLs: the route is mounted under /api (and is not
// swallowed by /charges/{id}), needs a signed-in user, answers an empty list without
// stores, and is in the shared per-user limit.
func TestChargesToConfirmThroughTheRouter(t *testing.T) {
	r := New(Deps{})
	uid := uuid.New()
	get := func(signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/charges/to-confirm", nil)
		if signedIn {
			req = req.WithContext(auth.WithUser(req.Context(), uid))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := get(false); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", w.Code)
	}
	w := get(true)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("signed in: %d %q cache=%q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		limited = get(true).Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Error("40 calls were never limited")
	}
}

func TestOpenNetworkStates(t *testing.T) {
	for s, want := range map[string]bool{"estimated": true, "needs_confirmation": true, "confirmed": false, "": false, "rejected": false} {
		if isOpenNetworkState(s) != want {
			t.Errorf("isOpenNetworkState(%q) != %v", s, want)
		}
	}
}
