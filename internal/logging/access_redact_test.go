package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// The signup magic-link token is a credential in the path; the access log (kept
// 90 days) must not carry it.
func TestAccessLogRedactsTheSignupToken(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	const token = "ABCDEFGHIJKLMNOPQRSTUV2345"
	req := httptest.NewRequest(http.MethodGet, "/api/signup/token/"+token, nil)
	ww := middleware.NewWrapResponseWriter(httptest.NewRecorder(), 1)
	ww.WriteHeader(200)
	AccessLog(req, ww, time.Millisecond, uuid.Nil, "")
	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("the token is in the access log: %s", out)
	}
	if !strings.Contains(out, "/api/signup/token/:redacted") {
		t.Errorf("no redacted path in the log line: %s", out)
	}

	// Other paths are logged as they are.
	buf.Reset()
	req = httptest.NewRequest(http.MethodGet, "/api/vehicles", nil)
	AccessLog(req, ww, time.Millisecond, uuid.Nil, "")
	if !strings.Contains(buf.String(), `"path":"/api/vehicles"`) {
		t.Errorf("ordinary path changed: %s", buf.String())
	}
	if loggedPath("/api/signup/token/") != "/api/signup/token/:redacted" || loggedPath("/api/signup/request") != "/api/signup/request" {
		t.Error("loggedPath")
	}
}
