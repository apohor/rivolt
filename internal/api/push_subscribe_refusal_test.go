package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An unfamiliar push service is refused with a class and the host (so the page can name it), and the log line carries the host and
// the user agent but never the path (a capability) or the keys.
func TestPushSubscribeRefusalNamesTheHostOnly(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	body := `{"endpoint":"https://unknown-push.example/fcm/send/CAPABILITY-TOKEN?x=1","keys":{"p256dh":"KEY-P256DH-SECRET","auth":"KEY-AUTH-SECRET"},"user_agent":"Mozilla/5.0 (Linux; Android 16) Chrome/130"}`
	rec := httptest.NewRecorder()
	handlePushSubscribe(nil)(rec, httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s, want 400", rec.Code, rec.Body.String())
	}
	var out struct{ Error, Class, Host string }
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Error != "unsupported push service" || out.Class != "unsupported_push_service" || out.Host != "unknown-push.example" {
		t.Errorf("body %s, want the error, class unsupported_push_service and the host", rec.Body.String())
	}
	line := logs.String()
	if !strings.Contains(line, "unknown-push.example") || !strings.Contains(line, "Android 16") {
		t.Errorf("the log lacks the host or the user agent: %s", line)
	}
	for _, secret := range []string{"CAPABILITY-TOKEN", "/fcm/send", "KEY-P256DH-SECRET", "KEY-AUTH-SECRET", "x=1"} {
		if strings.Contains(line, secret) || strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%q leaked into the log or the response: %s | %s", secret, line, rec.Body.String())
		}
	}
}
