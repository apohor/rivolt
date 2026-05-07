package kratos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// startTestServer returns an httptest.Server whose handler is the
// caller-supplied func, plus a *Client wired to talk to it.
func startTestServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, c
}

func TestNewFromEnv_disabled(t *testing.T) {
	t.Setenv("KRATOS_ADMIN_URL", "")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if c.Enabled() {
		t.Fatalf("expected disabled when KRATOS_ADMIN_URL is empty")
	}
}

func TestCreateIdentity_success(t *testing.T) {
	var got identityRequest
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/identities" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"00000000-0000-0000-0000-000000000001"}`))
	})

	if err := c.CreateIdentity(context.Background(), "u@example.com", "User One", "admin", "hunter2"); err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if got.SchemaID != "rivolt_user" {
		t.Errorf("schema_id = %q, want rivolt_user", got.SchemaID)
	}
	if got.Traits.Email != "u@example.com" {
		t.Errorf("email = %q", got.Traits.Email)
	}
	if got.Traits.DisplayName != "User One" {
		t.Errorf("display_name = %q", got.Traits.DisplayName)
	}
	if got.MetadataPublic.Role != "admin" {
		t.Errorf("role = %q", got.MetadataPublic.Role)
	}
	if got.Credentials == nil || got.Credentials.Password == nil ||
		got.Credentials.Password.Config.Password != "hunter2" {
		t.Errorf("password not propagated: %+v", got.Credentials)
	}
}

func TestCreateIdentity_conflict(t *testing.T) {
	_, c := startTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":409,"message":"conflict"}}`))
	})
	err := c.CreateIdentity(context.Background(), "dup@example.com", "", "user", "pw")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

func TestCreateIdentityGeneratePassword(t *testing.T) {
	var got identityRequest
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusCreated)
	})
	pw, err := c.CreateIdentityGeneratePassword(context.Background(), "x@example.com", "X", "user")
	if err != nil {
		t.Fatalf("CreateIdentityGeneratePassword: %v", err)
	}
	if len(pw) < 16 {
		t.Errorf("generated password too short: %q", pw)
	}
	if got.Credentials == nil || got.Credentials.Password.Config.Password != pw {
		t.Errorf("server-side password (%q) does not match returned (%q)",
			got.Credentials.Password.Config.Password, pw)
	}
}

func TestNormalizeRole(t *testing.T) {
	cases := map[string]string{
		"admin":   "admin",
		"Admins":  "admin",
		"  USER ": "user",
		"":        "user",
		"weird":   "user",
	}
	for in, want := range cases {
		if got := normalizeRole(in); got != want {
			t.Errorf("normalizeRole(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeleteIdentity_lookupAndDelete(t *testing.T) {
	deleted := false
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/identities":
			if got := r.URL.Query().Get("credentials_identifier"); got != "u@example.com" {
				t.Errorf("credentials_identifier = %q", got)
			}
			_, _ = w.Write([]byte(`[{"id":"abc","traits":{"email":"u@example.com"}}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/identities/abc":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := c.DeleteIdentity(context.Background(), "u@example.com"); err != nil {
		t.Fatalf("DeleteIdentity: %v", err)
	}
	if !deleted {
		t.Errorf("DELETE was not invoked")
	}
}

func TestDeleteIdentity_missingIsNoop(t *testing.T) {
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	})
	if err := c.DeleteIdentity(context.Background(), "missing@example.com"); err != nil {
		t.Errorf("expected no-op for missing user, got %v", err)
	}
}

func TestNilClientDisabled(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Fatalf("nil client must be disabled")
	}
	if err := c.CreateIdentity(context.Background(), "x", "", "user", "pw"); err == nil {
		t.Errorf("nil CreateIdentity should error")
	}
	if _, err := c.CreateIdentityGeneratePassword(context.Background(), "x", "", "user"); err == nil {
		t.Errorf("nil CreateIdentityGeneratePassword should error")
	}
	if err := c.DeleteIdentity(context.Background(), "x"); err == nil {
		t.Errorf("nil DeleteIdentity should error")
	}
}
