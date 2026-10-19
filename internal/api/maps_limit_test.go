package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/google/uuid"
)

func TestMapsUserLimiterIsPerUser(t *testing.T) {
	t.Parallel()
	h := mapsUserLimiter(2).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	do := func(uid uuid.UUID, addr string) int {
		r := httptest.NewRequest("POST", "/route", nil).WithContext(auth.WithUser(httptest.NewRequest("GET", "/", nil).Context(), uid))
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	a, b := uuid.New(), uuid.New()
	for i := 0; i < 2; i++ {
		if c := do(a, "1.1.1.1:1"); c != 204 {
			t.Fatalf("burst %d = %d", i, c)
		}
	}
	// Same user from a new IP is still limited; another user is not.
	if c := do(a, "2.2.2.2:1"); c != 429 {
		t.Fatalf("user a from new IP = %d, want 429", c)
	}
	if c := do(b, "1.1.1.1:1"); c != 204 {
		t.Fatalf("user b = %d, want 204", c)
	}
}
