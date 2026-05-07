package idp

import (
	"context"
	"fmt"

	"github.com/apohor/rivolt/internal/authelia"
)

// FromAuthelia wraps an *authelia.Client so it satisfies UserProvider.
// Returns nil when ac is nil (disabled), which is a valid UserProvider
// value — Enabled() returns false and all mutating calls return errors.
func FromAuthelia(ac *authelia.Client) UserProvider {
	if ac == nil || !ac.Enabled() {
		return disabled{}
	}
	return &autheliaProvider{c: ac}
}

type autheliaProvider struct{ c *authelia.Client }

func (a *autheliaProvider) Enabled() bool { return a.c.Enabled() }

func (a *autheliaProvider) CreateUser(ctx context.Context, req CreateRequest) error {
	return a.c.UpsertUserWithPassword(ctx, req.Username, req.Email, req.DisplayName, req.Role, req.Password)
}

func (a *autheliaProvider) CreateUserGeneratePassword(ctx context.Context, req CreateRequest) (string, error) {
	return a.c.UpsertUser(ctx, req.Username, req.Email, req.DisplayName, req.Role)
}

func (a *autheliaProvider) DeleteUser(ctx context.Context, username string) error {
	return a.c.DeleteUser(ctx, username)
}

// disabled is the no-op provider used when no backend is configured.
type disabled struct{}

func (disabled) Enabled() bool { return false }
func (disabled) CreateUser(_ context.Context, _ CreateRequest) error {
	return fmt.Errorf("idp: no provider configured")
}
func (disabled) CreateUserGeneratePassword(_ context.Context, _ CreateRequest) (string, error) {
	return "", fmt.Errorf("idp: no provider configured")
}
func (disabled) DeleteUser(_ context.Context, _ string) error {
	return fmt.Errorf("idp: no provider configured")
}
