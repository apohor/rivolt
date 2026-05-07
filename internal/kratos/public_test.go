package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// publicClient builds a Client whose AdminURL is unused but whose
// PublicURL points at a test server. Useful for the LoginByPassword
// and Whoami tests.
func publicClient(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: "http://unused", PublicURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, c
}

func TestLoginByPassword_success(t *testing.T) {
	flowID := "flow-123"
	_, c := publicClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/self-service/login/api":
			_, _ = w.Write([]byte(`{"id":"` + flowID + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/self-service/login":
			if got := r.URL.Query().Get("flow"); got != flowID {
				t.Errorf("flow query = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			var in map[string]string
			_ = json.Unmarshal(body, &in)
			if in["method"] != "password" || in["identifier"] != "u@example.com" || in["password"] != "secret" {
				t.Errorf("body = %+v", in)
			}
			_, _ = w.Write([]byte(`{"session":{"identity":{"id":"id-1","traits":{"email":"u@example.com","display_name":"U"}}}}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	id, err := c.LoginByPassword(context.Background(), "u@example.com", "secret")
	if err != nil {
		t.Fatalf("LoginByPassword: %v", err)
	}
	if id.ID != "id-1" || id.Traits.DisplayName != "U" {
		t.Errorf("identity: %+v", id)
	}
}

func TestLoginByPassword_invalidCredentials(t *testing.T) {
	_, c := publicClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"f"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400}}`))
	})
	_, err := c.LoginByPassword(context.Background(), "u", "bad")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginByPassword_requiresPublicURL(t *testing.T) {
	c, _ := New(Config{AdminURL: "http://unused"})
	_, err := c.LoginByPassword(context.Background(), "u", "p")
	if err == nil || !strings.Contains(err.Error(), "PublicURL") {
		t.Errorf("expected PublicURL error, got %v", err)
	}
}

func TestWhoami_active(t *testing.T) {
	_, c := publicClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/whoami" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "ory_kratos_session=abc" {
			t.Errorf("Cookie header = %q", got)
		}
		_, _ = w.Write([]byte(`{"identity":{"id":"id-1","traits":{"email":"u@x"}}}`))
	})
	id, err := c.Whoami(context.Background(), "ory_kratos_session=abc")
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if id.ID != "id-1" {
		t.Errorf("identity: %+v", id)
	}
}

func TestWhoami_noSession(t *testing.T) {
	_, c := publicClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Whoami(context.Background(), "irrelevant=1")
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession, got %v", err)
	}
}

func TestWhoami_emptyCookie(t *testing.T) {
	c, _ := New(Config{AdminURL: "http://x", PublicURL: "http://y"})
	_, err := c.Whoami(context.Background(), "")
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession on empty cookie, got %v", err)
	}
}
