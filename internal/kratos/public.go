package kratos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Identity is the subset of an identity object we surface from the
// public Kratos API. It mirrors the fields the admin POST /admin/
// identities response contains, but only the parts the Hydra login
// handler needs to make a "subject" decision.
type Identity struct {
	ID             string             `json:"id"`
	Traits         IdentityTraits     `json:"traits"`
	MetadataPublic IdentityMetaPublic `json:"metadata_public,omitempty"`
}

// IdentityTraits matches our rivolt_user schema (email + display).
type IdentityTraits struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

// IdentityMetaPublic carries fields we attach to the identity for
// display / authorization but don't want users to mutate via
// self-service flows. Kratos guarantees `metadata_public` is only
// writable from the admin API.
//
// Today we use it for `role` (admin / user), which the consent
// handler projects into a `groups` claim on the id_token so
// downstream OIDC clients (ArgoCD, Grafana, Rivolt) can run their
// own RBAC off it.
type IdentityMetaPublic struct {
	Role string `json:"role,omitempty"`
}

// LoginByPassword authenticates a user against Kratos's API login
// flow (no cookies, no CSRF). On success returns the Kratos identity
// (the .id is what we feed to Hydra's accept-login as `subject`).
//
// Returns ErrInvalidCredentials when Kratos rejects the password
// pair, so callers can surface a 401 to the browser without leaking
// whether the email or the password was wrong.
func (c *Client) LoginByPassword(ctx context.Context, email, password string) (*Identity, error) {
	if !c.Enabled() {
		return nil, errors.New("kratos: client not configured")
	}
	if c.cfg.PublicURL == "" {
		return nil, errors.New("kratos: PublicURL is required for LoginByPassword")
	}
	flow, err := c.createNativeLoginFlow(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]string{
		"method":     "password",
		"identifier": email,
		"password":   password,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	target := fmt.Sprintf("%s/self-service/login?flow=%s", c.cfg.PublicURL, flow)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kratos login: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Session struct {
				Identity Identity `json:"identity"`
			} `json:"session"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("decode login: %w", err)
		}
		if out.Session.Identity.ID == "" {
			return nil, errors.New("kratos: login response missing identity")
		}
		return &out.Session.Identity, nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		// 400: validation errors (bad credentials, missing fields).
		// 401/403: rejected. Treat all three as "credentials invalid"
		// to avoid timing/leak distinctions.
		return nil, ErrInvalidCredentials
	default:
		return nil, apiError("login by password", resp)
	}
}

// Whoami calls /sessions/whoami with the supplied cookie header
// (forwarded verbatim from the inbound request). Returns the
// identity if the session is active; ErrNoSession when Kratos
// returns 401 or the cookie is missing.
//
// The cookie value is the raw "Cookie:" header from the user's
// request — Kratos pulls its own session cookie out of it.
func (c *Client) Whoami(ctx context.Context, cookieHeader string) (*Identity, error) {
	if !c.Enabled() {
		return nil, errors.New("kratos: client not configured")
	}
	if c.cfg.PublicURL == "" {
		return nil, errors.New("kratos: PublicURL is required for Whoami")
	}
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return nil, ErrNoSession
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.cfg.PublicURL+"/sessions/whoami", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kratos whoami: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Identity Identity `json:"identity"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("decode whoami: %w", err)
		}
		if out.Identity.ID == "" {
			return nil, ErrNoSession
		}
		return &out.Identity, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrNoSession
	default:
		return nil, apiError("whoami", resp)
	}
}

// createNativeLoginFlow initialises a fresh API-style login flow.
// Returns the flow id; the caller submits credentials to
// /self-service/login?flow={id}.
func (c *Client) createNativeLoginFlow(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.cfg.PublicURL+"/self-service/login/api", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("kratos init login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiError("init login flow", resp)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode init: %w", err)
	}
	if out.ID == "" {
		return "", errors.New("kratos: empty flow id")
	}
	return out.ID, nil
}

// ErrInvalidCredentials is returned by LoginByPassword when Kratos
// rejects the email/password pair. Callers should surface a generic
// 401 to keep the response opaque.
var ErrInvalidCredentials = errors.New("kratos: invalid credentials")

// ErrNotFound is returned by admin lookups (GetIdentity) when the
// requested identity does not exist (Kratos returns 404). Distinct
// from ErrNoSession (no active session) and ErrInvalidCredentials
// (login failed): callers usually treat ErrNotFound as a hard 4xx
// because it means the subject we got from Hydra is stale.
var ErrNotFound = errors.New("kratos: identity not found")

// ErrNoSession is returned by Whoami when there is no active
// Kratos session for the supplied cookie.
var ErrNoSession = errors.New("kratos: no active session")
