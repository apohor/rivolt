package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTermsAcceptRejectsAVersionThatIsNotCurrent(t *testing.T) {
	rec := httptest.NewRecorder()
	handleTermsAccept(nil)(uuid.New(), rec, httptest.NewRequest("POST", "/api/terms/accept", strings.NewReader(`{"version":"2020-01-01"}`)))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"current":"`+currentTermsVersion+`"`) {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleTermsAccept(nil)(uuid.New(), rec, httptest.NewRequest("POST", "/api/terms/accept", strings.NewReader(`nope`)))
	if rec.Code != 400 {
		t.Errorf("bad json status %d", rec.Code)
	}
}

// Accepting is a write, so it is never served on the public demo allowlist.
func TestTermsAcceptIsNotOnTheDemoAllowlist(t *testing.T) {
	if demoAllowed("POST", "/api/terms/accept", "v") || demoOnlyPublic("POST", "/api/terms/accept", "v") {
		t.Error("POST /api/terms/accept is demo-allowed")
	}
}
