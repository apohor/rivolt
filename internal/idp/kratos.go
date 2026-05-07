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
// Username is preserved on CreateRequest for parity with the Authelia
// provider but is not used here; callers should pass the same value
// for Username and Email during the cutover.
func FromKratos(kc *kratos.Client) UserProvider {
	if kc == nil || !kc.Enabled() {
		return disabled{}
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
	// In our schema the email is the canonical identifier. Old call
	// sites still pass `username` (legacy from Authelia file backend
	// where username and email were distinct columns); accept either
	// — Kratos's lookup by credentials_identifier matches both since
	// the email is the identifier. If callers ever drift, this is
	// the seam to add a username→email mapping table.
	return k.c.DeleteIdentity(ctx, username)
}

// pickEmail returns the request email, falling back to username.
// This keeps the API stable while the Authelia and Kratos worlds
// run in parallel: the Authelia provider treated username as the
// login name and email as a separate trait, while Kratos uses email
// as the login identifier.
func pickEmail(req CreateRequest) string {
	if req.Email != "" {
		return req.Email
	}
	return req.Username
}
