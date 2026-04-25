package sessions

import (
	"context"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/google/uuid"
)

// AuthAdapter wraps *Store so it satisfies auth.SessionStore.
// The adapter lives here (not in the auth package) so the auth
// package stays free of a concrete sessions dependency; auth
// programs against its own SessionStore interface.
//
// The two tiny struct translations (AuthCreateOpts → CreateOpts,
// Session → auth.SessionInfo) are the whole reason this type
// exists — if the shapes ever converge we can delete the
// adapter.
type AuthAdapter struct{ *Store }

// NewAuthAdapter wraps an existing Store for use with
// auth.Service.WithSessionStore.
func NewAuthAdapter(s *Store) *AuthAdapter { return &AuthAdapter{Store: s} }

// Create implements auth.SessionStore.
func (a *AuthAdapter) Create(ctx context.Context, userID uuid.UUID, ttl time.Duration, opts auth.SessionCreateOpts) (auth.SessionInfo, string, error) {
	sess, raw, err := a.Store.Create(ctx, userID, ttl, CreateOpts{
		UserAgent:   opts.UserAgent,
		IPAddress:   SanitizeIP(opts.IPAddress),
		DeviceLabel: opts.DeviceLabel,
	})
	if err != nil {
		return auth.SessionInfo{}, "", err
	}
	return auth.SessionInfo{ID: sess.ID, UserID: sess.UserID}, raw, nil
}

// Lookup implements auth.SessionStore.
func (a *AuthAdapter) Lookup(ctx context.Context, rawTokenCookie string) (auth.SessionInfo, error) {
	sess, err := a.Store.Lookup(ctx, rawTokenCookie)
	if err != nil {
		return auth.SessionInfo{}, err
	}
	return auth.SessionInfo{ID: sess.ID, UserID: sess.UserID}, nil
}

// RevokeByToken implements auth.SessionStore.
func (a *AuthAdapter) RevokeByToken(ctx context.Context, rawTokenCookie string) error {
	return a.Store.RevokeByToken(ctx, rawTokenCookie)
}
