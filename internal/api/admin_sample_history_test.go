package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/demo"
)

// The admin action answers 403 from the writer's guard when the deployment is not the preview admin site, and 400 for a bad body.
func TestAdminSampleHistoryRefusesOnProd(t *testing.T) {
	prod := &demo.SampleHistoryWriter{Getenv: func(string) string { return "" }}
	h := handleAdminSampleHistory(prod)
	for body, want := range map[string]int{
		`{"user_id":"` + uuid.NewString() + `"}`:       http.StatusForbidden,
		`{"user_id":"` + demo.UserID().String() + `"}`: http.StatusForbidden,
		`{"user_id":"nope"}`:                           http.StatusBadRequest,
		`not json`:                                     http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/sample-history", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("%s: %d %s, want %d", body, rec.Code, rec.Body.String(), want)
		}
	}
}
