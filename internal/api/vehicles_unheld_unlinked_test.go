package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

// A pod that holds no tokens for an account answers the unheld client's 503 to a Rivian call. An account that never linked Rivian has no session
// to hold, so its list is the same empty 200 every pod gives; a linked or unreadable account keeps the retry-able 503.
func TestVehiclesOnAPodThatHoldsNoTokens(t *testing.T) {
	for _, tc := range []struct {
		name               string
		linked, reauth, ok bool
		wantStatus         int
		wantBody           string
	}{
		{"never linked", false, false, true, http.StatusOK, "[]"},
		{"state unreadable", false, false, false, http.StatusServiceUnavailable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fromDB := &vehiclesFromDB{account: func(context.Context) (bool, bool, bool) { return tc.linked, tc.reauth, tc.ok }}
			h := handleVehicles(rivian.NewUnheldClient(), nil, &sql.DB{}, nil, nil, nil, fromDB)
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantBody != "" && strings.TrimSpace(w.Body.String()) != tc.wantBody {
				t.Fatalf("body = %q, want %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}
