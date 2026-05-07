package idp_test

import (
	"context"
	"testing"

	"github.com/apohor/rivolt/internal/idp"
)

// TestDisabledProvider verifies that the Disabled provider never
// panics and always reports Enabled() == false.
func TestDisabledProvider(t *testing.T) {
	p := idp.Disabled()

	if p.Enabled() {
		t.Fatal("expected Enabled() == false for the disabled provider")
	}

	ctx := context.Background()

	if err := p.CreateUser(ctx, idp.CreateRequest{Username: "x"}); err == nil {
		t.Fatal("expected error from disabled CreateUser")
	}

	if pwd, err := p.CreateUserGeneratePassword(ctx, idp.CreateRequest{Username: "x"}); err == nil || pwd != "" {
		t.Fatalf("expected error and empty password from disabled CreateUserGeneratePassword, got %q %v", pwd, err)
	}

	if err := p.DeleteUser(ctx, "x"); err == nil {
		t.Fatal("expected error from disabled DeleteUser")
	}
}

// TestCreateRequestFields ensures CreateRequest carries all fields
// (compile-time check that the struct is fully accessible).
func TestCreateRequestFields(t *testing.T) {
	r := idp.CreateRequest{
		Username:    "alice",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		Role:        "user",
		Password:    "S3cr3t!pass",
	}
	if r.Username != "alice" || r.Role != "user" {
		t.Fatal("unexpected field values")
	}
}
