package idp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apohor/rivolt/internal/kratos"
)

func TestFromKratos_nilDisabled(t *testing.T) {
	p := FromKratos(nil)
	if p.Enabled() {
		t.Fatalf("nil should be disabled")
	}
	if err := p.CreateUser(context.Background(), CreateRequest{Email: "x@y"}); err == nil {
		t.Errorf("expected error from disabled provider")
	}
}

func TestFromKratos_emailFallsBackToUsername(t *testing.T) {
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back the path so we know we hit the right handler;
		// the body inspection lives in kratos package tests.
		got = r.URL.Path
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	kc, err := kratos.New(kratos.Config{AdminURL: srv.URL})
	if err != nil {
		t.Fatalf("kratos.New: %v", err)
	}
	p := FromKratos(kc)
	if !p.Enabled() {
		t.Fatalf("expected enabled")
	}
	if err := p.CreateUser(context.Background(), CreateRequest{
		Username: "legacy@user.dev",
		Password: "pw",
		Role:     "user",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got != "/admin/identities" {
		t.Errorf("unexpected path: %q", got)
	}
}
