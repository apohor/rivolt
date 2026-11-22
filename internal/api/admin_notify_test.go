package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/apohor/rivolt/internal/email"
)

type captureTransport struct {
	mu sync.Mutex
	to []string
}

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	var body struct {
		To []string `json:"to"`
	}
	_ = json.Unmarshal(b, &body)
	c.mu.Lock()
	c.to = append(c.to, body.To...)
	c.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"x"}`)), Header: http.Header{}, Request: r}, nil
}

// Operator notices (new access request, Rivian sign-in finished) go to
// RIVOLT_EMAIL_OPERATOR_NOTICE_TO when it is set, else to the From address.
func TestNotifyAdminRecipient(t *testing.T) {
	for _, c := range []struct{ name, noticeTo, want string }{
		{"unset: the From address", "", "support@rivolt.dev"},
		{"set: the notice address", "delivered+preview-ops@resend.dev", "delivered+preview-ops@resend.dev"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cap := &captureTransport{}
			old := http.DefaultTransport
			http.DefaultTransport = cap
			t.Cleanup(func() { http.DefaultTransport = old })
			m := email.New(email.Config{APIKey: "k", From: "Rivolt <support@rivolt.dev>", OperatorNoticeTo: c.noticeTo})
			notifyAdmin(context.Background(), m, nil, "subject", "text")
			if len(cap.to) != 1 || cap.to[0] != c.want {
				t.Fatalf("notice sent to %v, want [%s]", cap.to, c.want)
			}
		})
	}
	// No mailer: nothing is sent and nothing panics.
	notifyAdmin(context.Background(), nil, nil, "subject", "text")
}
