// Package email sends transactional mail via the Resend HTTP API.
//
// Rivolt previously had no direct email path — Kratos was the only
// component that talked to Resend, and it did so via SMTP. The
// signup-request approval flow needs Rivolt itself to send a one-off
// "you're in, here's your invite code" mail, so this package wraps
// Resend's REST endpoint with a tiny client.
//
// HTTP rather than SMTP keeps the dependency surface flat (no STARTTLS
// dance, no net/smtp) and reuses the same Resend account/sender domain
// that Kratos already authenticates against.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Counter is the metric-recording surface the Resend client uses
// without taking a hard dependency on internal/metrics (avoids a
// package-level import cycle: metrics imports nothing application,
// application imports metrics from main, email is a leaf).
// Implementations must be safe for concurrent use.
type Counter interface {
	IncEmailSend(provider, status string)
}

// Client posts to https://api.resend.com/emails.
type Client struct {
	apiKey  string
	from    string
	hc      *http.Client
	base    string // override target for tests
	counter Counter
}

// Config carries the construction parameters. From must be a verified
// sender on the Resend account (e.g. "Rivolt <hello@rivolt.dev>").
type Config struct {
	APIKey  string
	From    string
	Counter Counter // optional; nil is fine
}

// New returns a Client, or nil when APIKey/From are empty so the
// caller can wire the dependency optionally without a separate
// "disabled" type.
func New(cfg Config) *Client {
	if cfg.APIKey == "" || cfg.From == "" {
		return nil
	}
	return &Client{
		apiKey:  cfg.APIKey,
		from:    cfg.From,
		hc:      &http.Client{Timeout: 10 * time.Second},
		base:    "https://api.resend.com",
		counter: cfg.Counter,
	}
}

// Message is the shape Resend's /emails endpoint accepts. Only the
// fields the signup-approval template needs are exposed.
type Message struct {
	To      string
	Subject string
	Text    string // plain-text body; rendered as-is
	HTML    string // optional HTML alternative; nil-coalesced to "" if absent
}

// ErrNotConfigured is returned by Send when called on a nil Client so
// admin handlers can branch cleanly ("email not configured — copy the
// code manually") without a separate has-email check at every site.
var ErrNotConfigured = errors.New("email: client not configured")

// FromAddress returns the bare email parsed out of the configured
// `Name <addr>` From string (e.g. "Rivolt <anton@rivolt.dev>" →
// "anton@rivolt.dev"). Empty when the client is nil or when the From
// has no angle-bracketed address. Used by admin notifications so the
// same env var that gates the verified sender also targets the
// admin's inbox — no second env var to keep in sync.
func (c *Client) FromAddress() string {
	if c == nil {
		return ""
	}
	if i := strings.LastIndexByte(c.from, '<'); i >= 0 {
		if j := strings.IndexByte(c.from[i+1:], '>'); j >= 0 {
			return strings.TrimSpace(c.from[i+1 : i+1+j])
		}
	}
	return strings.TrimSpace(c.from)
}

// Send posts a single message. Returns nil on 2xx; the Resend body is
// surfaced verbatim in the error otherwise so admin logs can show why.
func (c *Client) Send(ctx context.Context, m Message) error {
	if c == nil {
		// "not_configured" lands as its own status so the alert
		// rule can tell "we tried to email but no client wired"
		// from "we tried and the provider barked".
		return ErrNotConfigured
	}
	body := map[string]any{
		"from":    c.from,
		"to":      []string{m.To},
		"subject": m.Subject,
		"text":    m.Text,
	}
	if m.HTML != "" {
		body["html"] = m.HTML
	}
	buf, err := json.Marshal(body)
	if err != nil {
		c.observe("error")
		return fmt.Errorf("email: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/emails", bytes.NewReader(buf))
	if err != nil {
		c.observe("error")
		return fmt.Errorf("email: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		c.observe("error")
		return fmt.Errorf("email: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.observe("ok")
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	// Rate-limit + quota signals get their own status so the
	// "approaching cap" alert can fire on the daily-volume
	// counter before we hit the actual wall.
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusPaymentRequired:
		c.observe("rate_limited")
	default:
		c.observe("error")
	}
	return fmt.Errorf("email: resend status %d: %s", resp.StatusCode, string(respBody))
}

// observe is the metric-emit shim; nil-safe so tests don't have to
// thread a counter through every fixture.
func (c *Client) observe(status string) {
	if c == nil || c.counter == nil {
		return
	}
	c.counter.IncEmailSend("resend", status)
}
