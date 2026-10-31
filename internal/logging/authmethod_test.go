package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// The access log says how an authenticated request got in, and adds nothing
// for an anonymous one.
func TestAccessLogCarriesTheAuthMethod(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	run := func(authenticate func(r *http.Request) *http.Request) map[string]any {
		buf.Reset()
		h := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticate != nil {
				authenticate(r)
			}
			w.WriteHeader(204)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/me", nil))
		var line map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
			t.Fatalf("access log line %q: %v", buf.String(), err)
		}
		return line
	}

	for _, method := range []string{AuthCookie, AuthKratos, AuthImpersonation, AuthBypass} {
		line := run(func(r *http.Request) *http.Request {
			ctx := WithAuthMethod(WithUserID(r.Context(), uuid.New()), method)
			return r.WithContext(ctx)
		})
		if line["auth_method"] != method || line["user_id"] == nil {
			t.Errorf("%s: %v", method, line)
		}
	}
	// No user: no method, no user_id.
	if line := run(nil); line["auth_method"] != nil || line["user_id"] != nil {
		t.Errorf("anonymous line carries auth fields: %v", line)
	}
	// An empty method stores nothing.
	if line := run(func(r *http.Request) *http.Request {
		return r.WithContext(WithAuthMethod(WithUserID(r.Context(), uuid.New()), ""))
	}); line["auth_method"] != nil {
		t.Errorf("empty method logged: %v", line)
	}
}
