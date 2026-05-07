package idp

import (
	"context"
	"fmt"

	"github.com/apohor/rivolt/internal/kratos"
)

// FromKratos wraps a *kratos.Client so it satisfies UserProvider.
// Returns the disabled provider when kc is nil or not configured —
// callers can wire this unconditionally.
//
// Email is the canonical identifier for Kratos identities (the
// schema registers email as the password credential identifier).
// Username on CreateRequest is accepted for backwards compatibility
// with older callers but is otherwise unused.
func FromKratos(kc *kratos.Client) UserProvider {
	if kc == nil || !kc.Enabled() {
		return Disabled()
	}
	return &kratosProvider{c: kc}
}

type kratosProvider struct{ c *kratos.Client }

func (k *kratosProvider) Enabled() bool { return k.c.Enabled() }

func (k *kratosProvider) CreateUser(ctx context.Context, req CreateRequest) error {
	email := pickEmail(req)
	if email == "" {
		return fmt.Errorf("idp/kratos: email is required")
	}
	return k.c.CreateIdentity(ctx, email, req.DisplayName, req.Role, req.Password)
}

func (k *kratosProvider) CreateUserGeneratePassword(ctx context.Context, req CreateRequest) (string, error) {
	email := pickEmail(req)
	if email == "" {
		return "", fmt.Errorf("idp/kratos: email is required")
	}
	return k.c.CreateIdentityGeneratePassword(ctx, email, req.DisplayName, req.Role)
}

func (k *kratosProvider) DeleteUser(ctx context.Context, username string) error {
	// In our schema the email is the canonical identifier. Older call
	// sites still pass `username` (legacy callers); accept either —
	// Kratos's lookup by credentials_identifier matches the email
	// directly. If callers ever drift, this is the seam to add a
	// username→email mapping table.
	return k.c.DeleteIdentity(ctx, username)
}

// pickEmail returns the request email, falling back to username.
// Older call-sites passed only username; new code should always set
// Email. Kratos uses email as the credential identifier.
func pickEmail(req CreateRequest) string {
	if req.Email != "" {
		return req.Email
	}
	return req.Username
}
