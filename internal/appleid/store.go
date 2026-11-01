// Package appleid keeps the link between a Rivolt account and its Sign in with Apple identity (App Store 4.8;
// architect's design, DA-approved Oct 24): the identity's stable `sub`, which Apple's server-to-server
// notifications use to name the user, and the refresh token, kept only so deleting the account can revoke it.
//
// The `sub` is in apple_identities; the refresh token is sealed in user_secrets ("apple.refresh_token") like the
// Rivian session. Neither is ever logged.
package appleid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apohor/rivolt/internal/secrets"
)

// SecretName is the user_secrets entry holding Apple's refresh token for a user.
const SecretName = "apple.refresh_token"

// ErrSubInUse is returned when an Apple identity is already linked to a different account.
var ErrSubInUse = errors.New("appleid: this Apple identity belongs to another account")

// Store links accounts to Apple identities. It works on the system pool (sign-in and Apple's notifications run
// before or outside a user's request scope).
type Store struct {
	db      *sql.DB
	secrets *secrets.Store
}

// New builds a Store.
func New(db *sql.DB, s *secrets.Store) *Store { return &Store{db: db, secrets: s} }

// Link records that userID signs in with the Apple identity sub, and keeps refreshToken (when Apple issued one) for
// revocation. Linking the same account again is fine (the token is replaced); a sub already linked to another
// account is ErrSubInUse, and an account already linked to another sub keeps its first link.
func (s *Store) Link(ctx context.Context, userID uuid.UUID, sub, refreshToken string) error {
	sub = strings.TrimSpace(sub)
	if userID == uuid.Nil || sub == "" {
		return errors.New("appleid: link needs a user and a sub")
	}
	var owner uuid.UUID
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO apple_identities (user_id, sub) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET user_id = apple_identities.user_id
		RETURNING user_id`, userID, sub).Scan(&owner)
	if err != nil {
		// The sub's UNIQUE: another account holds it.
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "apple_identities_sub_key" {
			return ErrSubInUse
		}
		return fmt.Errorf("appleid: link: %w", err)
	}
	var linked string
	if err := s.db.QueryRowContext(ctx, `SELECT sub FROM apple_identities WHERE user_id = $1`, userID).Scan(&linked); err != nil {
		return fmt.Errorf("appleid: link: %w", err)
	}
	if linked != sub {
		return ErrSubInUse // this account is linked to another Apple identity
	}
	if refreshToken != "" && s.secrets != nil {
		if err := s.secrets.Put(ctx, userID, SecretName, []byte(refreshToken)); err != nil {
			return fmt.Errorf("appleid: keep token: %w", err)
		}
	}
	return nil
}

// UserBySub returns the account an Apple identity is linked to, or false.
func (s *Store) UserBySub(ctx context.Context, sub string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM apple_identities WHERE sub = $1`, strings.TrimSpace(sub)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("appleid: lookup: %w", err)
	}
	return id, true, nil
}

// UsernameBySub returns the account key (users.username) of the account an Apple identity is linked to, or false.
func (s *Store) UsernameBySub(ctx context.Context, sub string) (string, bool, error) {
	var username string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.username FROM apple_identities a JOIN users u ON u.id = a.user_id
		WHERE a.sub = $1`, strings.TrimSpace(sub)).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("appleid: lookup: %w", err)
	}
	return username, true, nil
}

// RefreshToken returns the account's kept Apple refresh token, or "" when there is none.
func (s *Store) RefreshToken(ctx context.Context, userID uuid.UUID) (string, error) {
	if s.secrets == nil {
		return "", nil
	}
	b, err := s.secrets.Get(ctx, userID, SecretName)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("appleid: read token: %w", err)
	}
	return string(b), nil
}

// DropToken forgets the account's Apple refresh token (consent revoked, or revoked at deletion). The link stays.
func (s *Store) DropToken(ctx context.Context, userID uuid.UUID) error {
	if s.secrets == nil {
		return nil
	}
	if err := s.secrets.Delete(ctx, userID, SecretName); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return fmt.Errorf("appleid: drop token: %w", err)
	}
	return nil
}

// Linked reports whether the account has an Apple identity linked.
func (s *Store) Linked(ctx context.Context, userID uuid.UUID) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM apple_identities WHERE user_id = $1`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("appleid: linked: %w", err)
	}
	return true, nil
}

// Unlink removes the account's Apple identity and forgets the kept refresh token. The caller has revoked the token at Apple, or queued
// that, first: after this the token is gone from here. Unlinking an account that is not linked is not an error.
func (s *Store) Unlink(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM apple_identities WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("appleid: unlink: %w", err)
	}
	return s.DropToken(ctx, userID)
}
