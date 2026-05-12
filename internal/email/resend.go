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
	"time"
)

// Client posts to https://api.resend.com/emails.
type Client struct {
	apiKey string
	from   string
	hc     *http.Client
	base   string // override target for tests
}

// Config carries the construction parameters. From must be a verified
// sender on the Resend account (e.g. "Rivolt <hello@rivolt.dev>").
type Config struct {
	APIKey string
	From   string
}

// New returns a Client, or nil when APIKey/From are empty so the
// caller can wire the dependency optionally without a separate
// "disabled" type.
func New(cfg Config) *Client {
	if cfg.APIKey == "" || cfg.From == "" {
		return nil
	}
	return &Client{
		apiKey: cfg.APIKey,
		from:   cfg.From,
		hc:     &http.Client{Timeout: 10 * time.Second},
		base:   "https://api.resend.com",
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

// Send posts a single message. Returns nil on 2xx; the Resend body is
// surfaced verbatim in the error otherwise so admin logs can show why.
func (c *Client) Send(ctx context.Context, m Message) error {
	if c == nil {
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
		return fmt.Errorf("email: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/emails", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("email: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("email: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("email: resend status %d: %s", resp.StatusCode, string(respBody))
}
