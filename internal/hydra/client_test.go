package hydra

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
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
	t.Setenv("HYDRA_ADMIN_URL", "")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if c.Enabled() {
		t.Fatalf("expected disabled")
	}
}

func TestGetLoginRequest(t *testing.T) {
	_, c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("login_challenge"); got != "abc" {
			t.Errorf("login_challenge = %q", got)
		}
		if r.URL.Path != "/admin/oauth2/auth/requests/login" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"challenge":"abc","subject":"u1","skip":true,"client":{"client_id":"rivolt","client_name":"Rivolt"},"requested_scope":["openid","email"]}`))
	})
	out, err := c.GetLoginRequest(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetLoginRequest: %v", err)
	}
	if !out.Skip || out.Subject != "u1" || out.Client.ClientName != "Rivolt" {
		t.Errorf("unexpected: %+v", out)
	}
}

func TestAcceptLoginRequest(t *testing.T) {
	var got AcceptLoginRequest
	_, c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/admin/oauth2/auth/requests/login/accept" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/oauth2/callback?...."}`))
	})
	out, err := c.AcceptLoginRequest(context.Background(), "abc",
		AcceptLoginRequest{Subject: "u1", Remember: true, RememberFor: 3600})
	if err != nil {
		t.Fatalf("AcceptLoginRequest: %v", err)
	}
	if out.RedirectTo == "" {
		t.Errorf("missing redirect_to")
	}
	if got.Subject != "u1" || !got.Remember || got.RememberFor != 3600 {
		t.Errorf("server saw: %+v", got)
	}
}

func TestAcceptConsentRequest_grantsAllScopes(t *testing.T) {
	var got AcceptConsentRequest
	_, c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/cb"}`))
	})
	_, err := c.AcceptConsentRequest(context.Background(), "ch",
		AcceptConsentRequest{
			GrantScope:               []string{"openid", "email", "profile"},
			GrantAccessTokenAudience: []string{"argocd"},
			Remember:                 true,
			RememberFor:              3600,
		})
	if err != nil {
		t.Fatalf("AcceptConsentRequest: %v", err)
	}
	if len(got.GrantScope) != 3 {
		t.Errorf("scopes: %+v", got.GrantScope)
	}
	if len(got.GrantAccessTokenAudience) != 1 {
		t.Errorf("audience: %+v", got.GrantAccessTokenAudience)
	}
}

func TestNilClientDisabled(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Fatalf("nil must be disabled")
	}
	if _, err := c.GetLoginRequest(context.Background(), "x"); err == nil {
		t.Errorf("expected error from nil client")
	}
}

func TestErrorBubblesUp(t *testing.T) {
	_, c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	})
	if _, err := c.GetLoginRequest(context.Background(), "x"); err == nil {
		t.Errorf("expected error on 5xx")
	}
}
