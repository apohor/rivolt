package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

func TestAIConsentGateFailsClosed(t *testing.T) {
	t.Parallel()
	called := false
	h := requireAIConsentMW(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/api/trips/plan/advice", nil)
	req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called || w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"class":"ai_off"`) {
		t.Fatalf("called=%v code=%d body=%s", called, w.Code, w.Body.String())
	}
}

func TestAIConsentPutRefusedWhileViewingAs(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPut, "/api/settings/ai-consent", strings.NewReader(`{"enabled":true}`))
	req = req.WithContext(auth.WithImpersonation(req.Context(), uuid.New(), uuid.New()))
	w := httptest.NewRecorder()
	handleAIConsentPut(nil)(uuid.New(), w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d", w.Code)
	}
}
