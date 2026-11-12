package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
)

// Each opt-in PUT can queue a receipts sync, so the route has its own per-user limit (about 10 a minute): past it the real router answers 429
// before the handler runs.
func TestReceiptsOptInPutIsLimited(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	h := asSession(New(Deps{Auth: svc}), sessions.KindWeb)
	handled, limited := 0, 0
	for i := 0; i < 25; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "https://rivolt.dev/api/settings/rivian-receipts", strings.NewReader("not json"))
		req.RemoteAddr = "203.0.113.9:4000"
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusBadRequest: // reached the handler
			handled++
		case http.StatusTooManyRequests:
			limited++
			if rec.Header().Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
		default:
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	if handled < 10 || handled > 12 || limited == 0 {
		t.Errorf("%d handled and %d limited of 25, want about 10 handled then limited", handled, limited)
	}
}
