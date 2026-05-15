package email

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeCounter records IncEmailSend calls so a test can assert
// the right (provider, status) pair fired.
type fakeCounter struct {
	mu      sync.Mutex
	entries []struct{ Provider, Status string }
}

func (c *fakeCounter) IncEmailSend(provider, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, struct{ Provider, Status string }{provider, status})
}

func (c *fakeCounter) last() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 {
		return "", ""
	}
	e := c.entries[len(c.entries)-1]
	return e.Provider, e.Status
}

// newTestClient builds a Client wired against an httptest server
// so a test can pin the response status without hitting Resend.
func newTestClient(t *testing.T, status int, body string, counter Counter) (*Client, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// Token must be Bearer-formed for /emails — a test that
		// breaks the header would silently miss the upstream auth
		// surface, so assert on it.
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{APIKey: "test-key", From: "Test <test@example.com>", Counter: counter})
	if c == nil {
		t.Fatal("New returned nil")
	}
	// Steer the client at the local httptest server. The base
	// field is package-private and that's intentional — tests are
	// the only legitimate caller.
	c.base = srv.URL
	return c, &calls
}

// TestSend_OkIncrementsCounter pins the happy-path:
// 2xx → counter "ok", error nil, exactly one upstream call.
func TestSend_OkIncrementsCounter(t *testing.T) {
	counter := &fakeCounter{}
	c, calls := newTestClient(t, 200, `{"id":"abc"}`, counter)
	err := c.Send(context.Background(), Message{
		To: "user@example.com", Subject: "hi", Text: "body",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
	prov, status := counter.last()
	if prov != "resend" || status != "ok" {
		t.Errorf("counter = (%q,%q), want (resend, ok)", prov, status)
	}
}

// TestSend_429IsRateLimited pins the alert seam. The cap-approaching
// rule fires on status="rate_limited" — any future refactor that
// downgrades a 429 to a generic "error" silently disarms that alert.
func TestSend_429IsRateLimited(t *testing.T) {
	counter := &fakeCounter{}
	c, _ := newTestClient(t, http.StatusTooManyRequests, "monthly cap", counter)
	err := c.Send(context.Background(), Message{To: "u@example.com", Subject: "s", Text: "b"})
	if err == nil {
		t.Fatal("Send should surface a non-2xx as error")
	}
	if !strings.Contains(err.Error(), "monthly cap") {
		t.Errorf("error body not surfaced: %v", err)
	}
	if prov, status := counter.last(); prov != "resend" || status != "rate_limited" {
		t.Errorf("counter = (%q,%q), want (resend, rate_limited)", prov, status)
	}
}

// TestSend_402IsRateLimited — same intent as 429, but Resend signals
// "you've hit your plan" with 402 Payment Required on some accounts.
// Both must map to status="rate_limited" so the alert catches either.
func TestSend_402IsRateLimited(t *testing.T) {
	counter := &fakeCounter{}
	c, _ := newTestClient(t, http.StatusPaymentRequired, "upgrade required", counter)
	_ = c.Send(context.Background(), Message{To: "u@example.com", Subject: "s", Text: "b"})
	if _, status := counter.last(); status != "rate_limited" {
		t.Errorf("counter status = %q, want rate_limited", status)
	}
}

// TestSend_5xxIsError covers the generic non-rate-limit failure path.
func TestSend_5xxIsError(t *testing.T) {
	counter := &fakeCounter{}
	c, _ := newTestClient(t, http.StatusInternalServerError, "internal", counter)
	if err := c.Send(context.Background(), Message{To: "u@example.com", Subject: "s", Text: "b"}); err == nil {
		t.Fatal("Send should return an error on 5xx")
	}
	if _, status := counter.last(); status != "error" {
		t.Errorf("counter status = %q, want error", status)
	}
}

// TestSend_NilClient asserts the nil-Client contract that callers
// (notifyAdmin etc.) rely on. Must return ErrNotConfigured and
// must not panic; metric increment is skipped (nothing to count).
func TestSend_NilClient(t *testing.T) {
	var c *Client
	err := c.Send(context.Background(), Message{To: "u@example.com", Subject: "s", Text: "b"})
	if err != ErrNotConfigured {
		t.Errorf("nil-client Send: got %v want ErrNotConfigured", err)
	}
}

// TestFromAddress pins the "Name <addr>" parser. notifyAdmin
// targets the bare email so the configured sender doubles as
// the admin inbox; a regression here would silently break the
// new-user-connected notification (the value would land as an
// invalid "Name <addr>" To: header).
func TestFromAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Rivolt <hello@rivolt.dev>", "hello@rivolt.dev"},
		{"hello@rivolt.dev", "hello@rivolt.dev"},
		{"  Rivolt <hello@rivolt.dev>  ", "hello@rivolt.dev"},
		{"", ""},
	}
	for _, c := range cases {
		client := &Client{from: c.in}
		if got := client.FromAddress(); got != c.want {
			t.Errorf("FromAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
