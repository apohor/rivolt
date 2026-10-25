package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/sessions"
)

type recordingMailer struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *recordingMailer) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func homeRequest(kind string, uid uuid.UUID) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/api/settings/home-location", nil)
	ctx := auth.WithUser(r.Context(), uid)
	if kind != "" {
		ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: kind})
	}
	return r.WithContext(ctx)
}

// The account is emailed when the iPhone app changes its home, once an hour, without any place in the message.
func TestHomeChangeFromTheAppIsEmailed(t *testing.T) {
	mail := &recordingMailer{}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	n := &homeChangeNotice{mail: mail, now: func() time.Time { return now },
		lookup: func(context.Context, uuid.UUID) (string, error) { return "me@example.com", nil }}
	uid := uuid.New()

	n.afterHomeChange(homeRequest(sessions.KindWeb, uid)) // a browser change: no email
	n.afterHomeChange(homeRequest("", uid))
	if len(mail.sent) != 0 {
		t.Fatalf("a web change sent %d emails", len(mail.sent))
	}
	n.afterHomeChange(homeRequest(sessions.KindApp, uid))
	if len(mail.sent) != 1 || mail.sent[0].To != "me@example.com" {
		t.Fatalf("an app change sent %+v", mail.sent)
	}
	m := mail.sent[0]
	for _, bad := range []string{"latitude", "longitude", "address", "30.", "-97."} {
		if strings.Contains(strings.ToLower(m.Text+m.Subject), bad) {
			t.Errorf("the notice mentions %q: %s", bad, m.Text)
		}
	}
	if !strings.Contains(m.Text, "support@rivolt.dev") || !strings.Contains(m.Text, "restore") {
		t.Errorf("the notice should say what to do: %s", m.Text)
	}
	// A second change within the hour is not another email; after the hour it is; another account has its own clock.
	n.afterHomeChange(homeRequest(sessions.KindApp, uid))
	now = now.Add(30 * time.Minute)
	n.afterHomeChange(homeRequest(sessions.KindApp, uid))
	if len(mail.sent) != 1 {
		t.Errorf("%d emails inside the hour, want 1", len(mail.sent))
	}
	n.afterHomeChange(homeRequest(sessions.KindApp, uuid.New()))
	if len(mail.sent) != 2 {
		t.Errorf("another account: %d emails, want 2", len(mail.sent))
	}
	now = now.Add(31 * time.Minute)
	n.afterHomeChange(homeRequest(sessions.KindApp, uid))
	if len(mail.sent) != 3 {
		t.Errorf("after the hour: %d emails, want 3", len(mail.sent))
	}
	// No verified address, no mailer, no notifier: nothing happens, and nothing panics.
	none := &homeChangeNotice{mail: mail, lookup: func(context.Context, uuid.UUID) (string, error) { return "", nil }}
	none.afterHomeChange(homeRequest(sessions.KindApp, uuid.New()))
	(&homeChangeNotice{}).afterHomeChange(homeRequest(sessions.KindApp, uuid.New()))
	var nilNotice *homeChangeNotice
	nilNotice.afterHomeChange(homeRequest(sessions.KindApp, uuid.New()))
	if len(mail.sent) != 3 {
		t.Errorf("%d emails after the no-op cases", len(mail.sent))
	}
}

// DA: the typed address is never logged, at any level, on success or on an upstream error.
func TestHomeAddressSearchNeverLogsTheAddress(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	const address = "1600 Secretive Lane, Springfield"
	for name, status := range map[string]int{"ok": http.StatusOK, "upstream error": http.StatusInternalServerError} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status != http.StatusOK {
				http.Error(w, "boom "+r.URL.RawQuery, status)
				return
			}
			_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
		}))
		photon := geocoding.NewPhotonClient(up.URL)
		w := httptest.NewRecorder()
		handleHomeAddressSearch(photon)(w, httptest.NewRequest(http.MethodPost, "/api/settings/home-location/search", strings.NewReader(`{"q":"`+address+`"}`)))
		up.Close()
		if status == http.StatusOK && w.Code != 200 || status != http.StatusOK && w.Code != http.StatusBadGateway {
			t.Errorf("%s: status %d", name, w.Code)
		}
		if strings.Contains(w.Body.String(), "Secretive") {
			t.Errorf("%s: the response echoes the address: %s", name, w.Body.String())
		}
	}
	if strings.Contains(logs.String(), "Secretive") || strings.Contains(logs.String(), "Springfield") {
		t.Errorf("the address reached the logs: %s", logs.String())
	}
}
