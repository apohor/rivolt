// Package idp defines the UserProvider interface — the single contract
// between Rivolt's API layer and whichever identity backend is deployed.
//
// Today the backend is Ory Kratos. The interface stays minimal so a
// future swap (Keycloak, Zitadel, lldap, …) lands behind the same
// surface: implement the interface, swap the wire in api.Config, done.
package idp

import (
	"context"
	"fmt"
)

// UserProvider provisions and deprovisions user identities in the
// configured identity backend. All methods must be safe to call
// concurrently. Implementations must also be safe to call when
// disabled (Enabled() == false) — they should return an error rather
// than panic.
type UserProvider interface {
	// Enabled reports whether the provider is configured. Safe to call
	// on a nil/zero value — must return false rather than panic.
	Enabled() bool

	// CreateUser provisions a new identity with a caller-supplied
	// password. This is the self-service signup path where the user
	// already chose their own password.
	//
	// Role must be "admin" or "user"; unknown values map to "user".
	// Returns an error if the user already exists.
	CreateUser(ctx context.Context, req CreateRequest) error

	// CreateUserGeneratePassword provisions a new identity and
	// generates a random one-time password. This is the admin-created
	// user path: the caller displays the returned password to the
	// admin once; it is never stored by Rivolt.
	//
	// Returns (password, error). On partial failure (user written to
	// the backend but a downstream sync failed) both may be non-zero —
	// callers should treat a non-empty password as success and log the
	// error as a warning.
	CreateUserGeneratePassword(ctx context.Context, req CreateRequest) (password string, err error)

	// DeleteUser removes a user identity. No-op when the user does
	// not exist.
	DeleteUser(ctx context.Context, username string) error
}

// CreateRequest carries the fields needed to provision a new user.
type CreateRequest struct {
	Username    string
	Email       string
	DisplayName string
	Role        string // "admin" | "user"
	Password    string // required for CreateUser; ignored by CreateUserGeneratePassword
}

// Disabled returns a no-op UserProvider whose Enabled() == false and
// every mutating call returns an error. Safe to wire into deployments
// that don't have an identity backend configured (local dev, smoke
// tests).
func Disabled() UserProvider { return disabled{} }

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
