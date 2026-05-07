// Package kratos provides admin-side provisioning of Ory Kratos
// identities via the Kratos admin API.
//
// Wire flow (vs the authelia package this replaces):
//
//	Admin → POST /api/admin/users
//	  rivolt API
//	    └─ kratos.Client.CreateIdentity
//	         └─ POST {AdminURL}/admin/identities
//	              ├─ traits.email = email
//	              ├─ traits.display_name = displayName
//	              ├─ schema_id = "rivolt_user"
//	              └─ credentials.password.config.password = <plaintext>
//
// Kratos hashes the password server-side (argon2id by default) and
// stores the identity in its Postgres DB. No Vault round-trip, no
// ExternalSecret refresh, no file-watcher polling — the identity is
// usable in <100 ms.
//
// All operations are idempotent in the sense that the API surface
// matches the authelia package: CreateIdentity returns an error on
// 409 Conflict (existing email), DeleteIdentity is a no-op on 404.
//
// The package is a no-op when not configured (NewFromEnv returns a
// nil client when KRATOS_ADMIN_URL is unset). Callers must check
// Enabled() before relying on it.
package kratos

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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

// Config holds runtime knobs for the Kratos provisioning client.
//
// Defaults match our in-cluster layout (see rivolt-infra
// apps/ory/kratos/values.yaml) so a typical deployment only needs
// to set AdminURL.
type Config struct {
	// AdminURL is the Kratos admin API base, e.g.
	// http://ory-kratos-admin.ory.svc:80. Required (and the only
	// required field for in-cluster identity-provisioning setups).
	AdminURL string

	// PublicURL is the Kratos public API base, e.g.
	// http://ory-kratos-public.ory.svc:80. Optional; only needed
	// when the caller uses LoginByPassword or Whoami (the Hydra
	// custom login UI in internal/api/hydra.go does).
	PublicURL string

	// SchemaID is the identity schema to use; matches the id in
	// kratos.config.identity.schemas[].id in the Helm values.
	SchemaID string

	// Timeout for individual HTTP calls to the admin API.
	Timeout time.Duration

	// AdminGroups / UserGroups are kept here for symmetry with the
	// authelia package — Kratos schemas don't have a built-in
	// "groups" concept, so we store the role in a metadata_public
	// field. Use the first entry of each slice as the canonical name.
	AdminGroups []string
	UserGroups  []string

	// HTTPClient is optional; one is constructed from Timeout when nil.
	HTTPClient *http.Client
}

// Client talks to the Kratos admin API. The zero value is not usable;
// build via NewFromEnv or New. A nil *Client is safe to call —
// Enabled() returns false and all mutating methods return errors.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewFromEnv builds a Client from environment variables. Returns a
// nil *Client (and nil error) when KRATOS_ADMIN_URL is unset, so
// callers can wire up the IdP unconditionally and rely on Enabled()
// to gate behavior.
//
// Recognised env vars:
//
//	KRATOS_ADMIN_URL          — required to enable
//	KRATOS_PUBLIC_URL         — optional; enables LoginByPassword/Whoami
//	KRATOS_SCHEMA_ID          — defaults to "rivolt_user"
//	KRATOS_TIMEOUT            — Go duration; default 10s
//	KRATOS_GROUPS_ADMIN       — CSV; default "admins"
//	KRATOS_GROUPS_USER        — CSV; default "users"
func NewFromEnv() (*Client, error) {
	addr := strings.TrimSpace(os.Getenv("KRATOS_ADMIN_URL"))
	if addr == "" {
		return nil, nil
	}
	timeout := 10 * time.Second
	if v := os.Getenv("KRATOS_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("KRATOS_TIMEOUT: %w", err)
		}
		timeout = d
	}
	return New(Config{
		AdminURL:    addr,
		PublicURL:   strings.TrimSpace(os.Getenv("KRATOS_PUBLIC_URL")),
		SchemaID:    envOr("KRATOS_SCHEMA_ID", "rivolt_user"),
		Timeout:     timeout,
		AdminGroups: splitCSV(envOr("KRATOS_GROUPS_ADMIN", "admins")),
		UserGroups:  splitCSV(envOr("KRATOS_GROUPS_USER", "users")),
	})
}

// New builds a Client from an explicit Config. Returns an error if
// AdminURL is empty.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.AdminURL) == "" {
		return nil, errors.New("kratos: AdminURL is required")
	}
	cfg.AdminURL = strings.TrimRight(cfg.AdminURL, "/")
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.SchemaID == "" {
		cfg.SchemaID = "rivolt_user"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if len(cfg.AdminGroups) == 0 {
		cfg.AdminGroups = []string{"admins"}
	}
	if len(cfg.UserGroups) == 0 {
		cfg.UserGroups = []string{"users"}
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

// Enabled reports whether the client is configured. Safe on nil.
func (c *Client) Enabled() bool { return c != nil }

// CreateIdentity provisions a new identity with a caller-supplied
// password. Returns an error if an identity with this email already
// exists (Kratos returns 409 Conflict).
func (c *Client) CreateIdentity(ctx context.Context, email, displayName, role, password string) error {
	if !c.Enabled() {
		return errors.New("kratos: client not configured")
	}
	if password == "" {
		return errors.New("kratos: password is required")
	}
	return c.create(ctx, email, displayName, role, password)
}

// CreateIdentityGeneratePassword provisions a new identity with a
// freshly generated random password and returns the password to the
// caller. The password is never stored by Rivolt.
func (c *Client) CreateIdentityGeneratePassword(ctx context.Context, email, displayName, role string) (string, error) {
	if !c.Enabled() {
		return "", errors.New("kratos: client not configured")
	}
	pw, err := generatePassword()
	if err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	if err := c.create(ctx, email, displayName, role, pw); err != nil {
		return "", err
	}
	return pw, nil
}

// DeleteIdentity removes the identity matching the given email.
// No-op when the identity does not exist (404). Email is the
// canonical login identifier in our schema.
func (c *Client) DeleteIdentity(ctx context.Context, email string) error {
	if !c.Enabled() {
		return errors.New("kratos: client not configured")
	}
	id, err := c.lookupIDByEmail(ctx, email)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	target := fmt.Sprintf("%s/admin/identities/%s", c.cfg.AdminURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kratos delete: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return apiError("delete identity", resp)
	}
}

// create POSTs to /admin/identities with traits + password credential.
func (c *Client) create(ctx context.Context, email, displayName, role, password string) error {
	role = normalizeRole(role)
	body := identityRequest{
		SchemaID: c.cfg.SchemaID,
		Traits: identityTraits{
			Email:       email,
			DisplayName: displayName,
		},
		MetadataPublic: metadataPublic{Role: role},
		Credentials: &identityCredentials{
			Password: &passwordCredential{
				Config: passwordConfig{Password: password},
			},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	target := c.cfg.AdminURL + "/admin/identities"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kratos create: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("kratos: identity already exists for %q", email)
	default:
		return apiError("create identity", resp)
	}
}

// lookupIDByEmail finds an identity ID by its email trait. Returns
// "" with nil error when no match exists.
func (c *Client) lookupIDByEmail(ctx context.Context, email string) (string, error) {
	// Kratos supports filtering by credentials_identifier — the email
	// in our schema is registered as the password credential identifier.
	q := url.Values{"credentials_identifier": []string{email}}
	target := c.cfg.AdminURL + "/admin/identities?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("kratos list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiError("list identities", resp)
	}
	var items []struct {
		ID     string         `json:"id"`
		Traits identityTraits `json:"traits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	for _, it := range items {
		if strings.EqualFold(it.Traits.Email, email) {
			return it.ID, nil
		}
	}
	return "", nil
}

// --- request/response types --------------------------------------

type identityRequest struct {
	SchemaID       string               `json:"schema_id"`
	Traits         identityTraits       `json:"traits"`
	MetadataPublic metadataPublic       `json:"metadata_public,omitempty"`
	Credentials    *identityCredentials `json:"credentials,omitempty"`
}

type identityTraits struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

type metadataPublic struct {
	Role string `json:"role,omitempty"`
}

type identityCredentials struct {
	Password *passwordCredential `json:"password,omitempty"`
}

type passwordCredential struct {
	Config passwordConfig `json:"config"`
}

type passwordConfig struct {
	Password string `json:"password"`
}

// --- helpers -----------------------------------------------------

func apiError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("kratos %s: status=%d body=%s", op, resp.StatusCode, truncate(body, 512))
}

func generatePassword() (string, error) {
	const rawBytes = 16
	b := make([]byte, rawBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "admins":
		return "admin"
	default:
		return "user"
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
