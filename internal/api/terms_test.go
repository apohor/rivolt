package api

import (
	"time"

	"github.com/apohor/rivolt/internal/db"
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

// Only accounts created on or after the effective time are asked; existing
// accounts are never gated, and an accepted current version clears it.
func TestTermsRequiredOnlyForNewAccounts(t *testing.T) {
	cur, old := currentTermsVersion, "2020-01-01"
	before := termsEffectiveAt.Add(-time.Hour)
	after := termsEffectiveAt.Add(time.Hour)
	for name, tc := range map[string]struct {
		t    db.Terms
		want bool
	}{
		"existing, never accepted": {db.Terms{CreatedAt: before}, false},
		"new, never accepted":      {db.Terms{CreatedAt: after}, true},
		"new at the exact moment":  {db.Terms{CreatedAt: termsEffectiveAt}, true},
		"new, accepted current":    {db.Terms{CreatedAt: after, Version: &cur}, false},
		"new, accepted an old one": {db.Terms{CreatedAt: after, Version: &old}, true},
		"unknown account":          {db.Terms{}, false},
	} {
		if got := termsRequired(tc.t); got != tc.want {
			t.Errorf("%s: required = %v, want %v", name, got, tc.want)
		}
	}
}
