// Package hydra is a thin admin-API client for Ory Hydra. We use it
// to drive the four endpoints that a custom login/consent UI must
// hit during an OAuth2/OIDC flow:
//
//	GET  /admin/oauth2/auth/requests/login    — read challenge
//	PUT  /admin/oauth2/auth/requests/login/accept   — finish login
//	GET  /admin/oauth2/auth/requests/consent  — read challenge
//	PUT  /admin/oauth2/auth/requests/consent/accept — finish consent
//
// The package is a no-op when not configured (NewFromEnv returns a
// nil client when HYDRA_ADMIN_URL is unset). Callers must check
// Enabled() before relying on it.
//
// We deliberately do not pull in github.com/ory/hydra-client-go: the
// surface we need is tiny, and the upstream client adds a transitive
// dependency tree larger than the rest of internal/* combined. The
// raw structs below cover only the fields we read.
package hydra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config holds runtime knobs for the Hydra admin client.
type Config struct {
	// AdminURL is the Hydra admin API base, e.g.
	// http://ory-hydra-admin.ory.svc:4445. Required.
	AdminURL string

	// Timeout for individual HTTP calls. Default 10s.
	Timeout time.Duration

	// HTTPClient is optional; one is constructed from Timeout when nil.
	HTTPClient *http.Client
}

// Client is the Hydra admin client. nil is safe — Enabled() returns
// false and all calls return an error rather than panic.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewFromEnv builds a Client from environment variables. Returns
// (nil, nil) when HYDRA_ADMIN_URL is unset so callers can wire it
// up unconditionally.
//
// Recognised env vars:
//
//	HYDRA_ADMIN_URL      — required to enable
//	HYDRA_TIMEOUT        — Go duration; default 10s
func NewFromEnv() (*Client, error) {
	addr := strings.TrimSpace(os.Getenv("HYDRA_ADMIN_URL"))
	if addr == "" {
		return nil, nil
	}
	timeout := 10 * time.Second
	if v := os.Getenv("HYDRA_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("HYDRA_TIMEOUT: %w", err)
		}
		timeout = d
	}
	return New(Config{AdminURL: addr, Timeout: timeout})
}

// New builds a Client from an explicit Config.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.AdminURL) == "" {
		return nil, errors.New("hydra: AdminURL is required")
	}
	cfg.AdminURL = strings.TrimRight(cfg.AdminURL, "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

// Enabled reports whether the client is configured. Safe on nil.
func (c *Client) Enabled() bool { return c != nil }

// LoginRequest is the subset of the Hydra login challenge we read.
// See https://www.ory.com/docs/hydra/reference/api for the full list.
type LoginRequest struct {
	Challenge      string      `json:"challenge"`
	Subject        string      `json:"subject"`
	Skip           bool        `json:"skip"`
	RequestURL     string      `json:"request_url"`
	RequestedScope []string    `json:"requested_scope"`
	Client         ClientInfo  `json:"client"`
	OIDCContext    OIDCContext `json:"oidc_context"`
	SessionID      string      `json:"session_id"`
}

// ConsentRequest is the subset of the Hydra consent challenge we read.
type ConsentRequest struct {
	Challenge                    string                 `json:"challenge"`
	Subject                      string                 `json:"subject"`
	Skip                         bool                   `json:"skip"`
	Client                       ClientInfo             `json:"client"`
	RequestedScope               []string               `json:"requested_scope"`
	RequestedAccessTokenAudience []string               `json:"requested_access_token_audience"`
	OIDCContext                  OIDCContext            `json:"oidc_context"`
	Context                      map[string]any         `json:"context"`
	Acr                          string                 `json:"acr"`
	Amr                          []string               `json:"amr"`
	GrantedScope                 []string               `json:"granted_scope"`
	IdentityClaims               map[string]any         `json:"-"`
	LoginChallenge               string                 `json:"login_challenge"`
	LoginSessionID               string                 `json:"login_session_id"`
	IDTokenHintClaims            map[string]any         `json:"id_token_hint_claims"`
}

// ClientInfo is the subset of the OAuth2 client metadata we surface
// to the login UI (mostly the display name).
type ClientInfo struct {
	ClientID   string   `json:"client_id"`
	ClientName string   `json:"client_name"`
	Scope      string   `json:"scope"`
	GrantTypes []string `json:"grant_types"`
}

// OIDCContext is the OIDC parameters set on the original auth
// request — `id_token_hint`, `display`, `login_hint`, `ui_locales`.
type OIDCContext struct {
	ACRValues         []string       `json:"acr_values"`
	UILocales         []string       `json:"ui_locales"`
	Display           string         `json:"display"`
	IDTokenHintClaims map[string]any `json:"id_token_hint_claims"`
	LoginHint         string         `json:"login_hint"`
}

// AcceptLoginRequest is the body for PUT
// /admin/oauth2/auth/requests/login/accept.
type AcceptLoginRequest struct {
	Subject     string         `json:"subject"`
	Remember    bool           `json:"remember"`
	RememberFor int64          `json:"remember_for,omitempty"`
	ACR         string         `json:"acr,omitempty"`
	AMR         []string       `json:"amr,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
}

// AcceptConsentRequest is the body for PUT
// /admin/oauth2/auth/requests/consent/accept.
type AcceptConsentRequest struct {
	GrantScope               []string `json:"grant_scope"`
	GrantAccessTokenAudience []string `json:"grant_access_token_audience,omitempty"`
	Remember                 bool     `json:"remember"`
	RememberFor              int64    `json:"remember_for,omitempty"`
	Session                  *Session `json:"session,omitempty"`
}

// Session is the optional id_token / access_token claim payload
// returned to the client. We send the bare minimum (sub claim is
// implicit) and let downstream apps query Kratos for richer profile.
type Session struct {
	IDToken     map[string]any `json:"id_token,omitempty"`
	AccessToken map[string]any `json:"access_token,omitempty"`
}

// Redirect is the response shape from any /accept endpoint.
type Redirect struct {
	RedirectTo string `json:"redirect_to"`
}

// GetLoginRequest fetches the login challenge from Hydra.
func (c *Client) GetLoginRequest(ctx context.Context, challenge string) (*LoginRequest, error) {
	if !c.Enabled() {
		return nil, errors.New("hydra: client not configured")
	}
	q := url.Values{"login_challenge": []string{challenge}}
	target := c.cfg.AdminURL + "/admin/oauth2/auth/requests/login?" + q.Encode()
	var out LoginRequest
	if err := c.do(ctx, http.MethodGet, target, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AcceptLoginRequest finishes the login phase and returns the URL
// the user-agent must be redirected to (typically on to consent).
func (c *Client) AcceptLoginRequest(ctx context.Context, challenge string, body AcceptLoginRequest) (*Redirect, error) {
	if !c.Enabled() {
		return nil, errors.New("hydra: client not configured")
	}
	q := url.Values{"login_challenge": []string{challenge}}
	target := c.cfg.AdminURL + "/admin/oauth2/auth/requests/login/accept?" + q.Encode()
	var out Redirect
	if err := c.do(ctx, http.MethodPut, target, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetConsentRequest fetches the consent challenge from Hydra.
func (c *Client) GetConsentRequest(ctx context.Context, challenge string) (*ConsentRequest, error) {
	if !c.Enabled() {
		return nil, errors.New("hydra: client not configured")
	}
	q := url.Values{"consent_challenge": []string{challenge}}
	target := c.cfg.AdminURL + "/admin/oauth2/auth/requests/consent?" + q.Encode()
	var out ConsentRequest
	if err := c.do(ctx, http.MethodGet, target, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AcceptConsentRequest finishes the consent phase. For a first-party
// app every requested scope is auto-granted by the caller.
func (c *Client) AcceptConsentRequest(ctx context.Context, challenge string, body AcceptConsentRequest) (*Redirect, error) {
	if !c.Enabled() {
		return nil, errors.New("hydra: client not configured")
	}
	q := url.Values{"consent_challenge": []string{challenge}}
	target := c.cfg.AdminURL + "/admin/oauth2/auth/requests/consent/accept?" + q.Encode()
	var out Redirect
	if err := c.do(ctx, http.MethodPut, target, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do is the shared transport: marshals reqBody to JSON when non-nil,
// decodes a 2xx response into respOut, surfaces 4xx/5xx as a typed
// error including the response body for debuggability.
func (c *Client) do(ctx context.Context, method, target string, reqBody, respOut any) error {
	var body io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hydra %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("hydra %s %s: status=%d body=%s",
			method, target, resp.StatusCode, truncate(raw, 512))
	}
	if respOut == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(respOut); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
