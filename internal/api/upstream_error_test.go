package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

// A persisted needs_reauth 401 must carry class="user_action" so the
// SPA treats it as "reconnect your Rivian" and does NOT bounce to
// /login. Without the class the browser loops between the app and the
// login page (regression surfaced by impersonating a needs_reauth
// user). ErrNeedsReauth is a bare sentinel, so this also covers the
// wrapped form the client returns.
func TestWriteUpstreamError_NeedsReauthCarriesClass(t *testing.T) {
	cases := map[string]error{
		"bare":    rivian.ErrNeedsReauth,
		"wrapped": fmt.Errorf("vehicles: %w", rivian.ErrNeedsReauth),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeUpstreamError(w, err)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			var body map[string]any
			if jerr := json.Unmarshal(w.Body.Bytes(), &body); jerr != nil {
				t.Fatalf("decode body: %v", jerr)
			}
			if body["class"] != "user_action" {
				t.Errorf("class = %v, want user_action (SPA keys the no-redirect path on this)", body["class"])
			}
		})
	}
}
