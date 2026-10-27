package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
)

// An app write is audited by who, from where, which group and which fields, and never by value.
func TestSettingsWriteAuditLineNamesTheSourceAndNoValues(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	uid := uuid.New()
	for kind, want := range map[string]string{sessions.KindApp: "source=app", sessions.KindWeb: "source=web"} {
		logs.Reset()
		req := httptest.NewRequest(http.MethodPut, "/api/settings/charging/offpeak", strings.NewReader(`{"windows":[{"rate":0.0123}]}`))
		ctx := auth.WithSession(auth.WithUser(context.Background(), uid), auth.SessionRef{ID: uuid.New(), Kind: kind})
		logSettingsWrite(req.WithContext(ctx), "charging.offpeak", "windows")
		line := logs.String()
		if !strings.Contains(line, want) || !strings.Contains(line, "user_id="+uid.String()) || !strings.Contains(line, "charging.offpeak") || !strings.Contains(line, "windows") {
			t.Errorf("%s: %s", kind, line)
		}
		if strings.Contains(line, "0.0123") {
			t.Errorf("a value reached the audit line: %s", line)
		}
	}
	// No user: nothing is logged.
	logs.Reset()
	logSettingsWrite(httptest.NewRequest(http.MethodPut, "/x", nil), "charging.offpeak", "windows")
	if logs.Len() != 0 {
		t.Errorf("a write with no user was logged: %s", logs.String())
	}
}

// 30 writes a minute per user on each of the newly opened routes, and a browser session counts the same.
func TestOpenedChargingWritesAreLimitedPerUser(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/settings/charging/offpeak"},
		{"PUT", "/api/settings/charging/networks"},
		{"POST", "/api/settings/charging/networks/reset"},
	} {
		h := asSession(New(Deps{Auth: svc}), sessions.KindApp) // one user per route
		passed, limited := 0, 0
		for i := 0; i < 60; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(c.method, "https://rivolt.dev"+c.path, strings.NewReader(`{}`))
			req.RemoteAddr = "203.0.113.9:4000"
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				limited++
			} else {
				passed++
			}
		}
		if passed < 30 || passed > 32 || limited == 0 {
			t.Errorf("%s %s: %d passed and %d limited of 60, want about 30 then limited", c.method, c.path, passed, limited)
		}
	}
}
