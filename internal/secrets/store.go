// Package secrets is the persistence layer on top of
// [`internal/crypto`](../crypto). It hides the Seal/Open plumbing
// and the user_secrets schema behind a tiny Put/Get/Delete API
// so feature code (rivian session, future AI keys, future push
// subscription private key) doesn't have to know either exists.
//
// # Why a package, not a function on the sealer
//
//   - The sealer is a pure crypto primitive; it has no opinion
//     on where blobs live. Keeping storage in a separate package
//     lets the phase-3 KMSSealer swap in without touching any
//     caller.
//   - Each caller wants the same behaviour ("store this blob
//     under this user, that name"); inlining sql.Exec at every
//     site would duplicate the kek_id audit stamp, the
//     ON CONFLICT upsert, and the GDPR-delete handling.
//
// # Naming convention for secret names
//
// The `name` column is free-text but callers are expected to
// use dot-delimited namespaced identifiers so a grep of the
// codebase turns up every producer/consumer of a given secret:
//
//   - rivian.session    — rivian.Session JSON blob
//   - ai.openai_key     — OpenAI API key (future)
//   - push.vapid_priv   — VAPID private key per user (future)
package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/google/uuid"
)

// ErrNotFound is returned by Get when no row exists for
// (userID, name). Distinct from an open/decrypt failure — a
// missing row is expected (user hasn't linked Rivian yet, etc.)
// and callers usually translate it to a zero value.
var ErrNotFound = errors.New("secrets: not found")

// Store wraps *sql.DB with Seal/Open plumbing. Safe for
// concurrent use — the underlying DB pool handles concurrency,
// and the sealer is stateless.
type Store struct {
	db     *sql.DB
	sealer crypto.Sealer
}

// New builds a Store. Both arguments are required; passing a
// NoopSealer is allowed but only for tests and is logged at
// OpenStore callers so nobody accidentally ships with it.
func New(db *sql.DB, sealer crypto.Sealer) *Store {
	return &Store{db: db, sealer: sealer}
}

// Put writes (userID, name) = plaintext, encrypted with the
// configured sealer. Upserts on (user_id, name) so the row's
// updated_at tracks the latest write and rotation tooling can
// re-wrap in place with the same primary key.
//
// The plaintext is sealed with the caller's userID bound as
// AAD, so a later Get with a different userID will refuse to
// open the row even if the attacker can read the raw blob.
func (s *Store) Put(ctx context.Context, userID uuid.UUID, name string, plaintext []byte) error {
	if userID == uuid.Nil {
		return errors.New("secrets: nil userID")
	}
	if name == "" {
		return errors.New("secrets: empty name")
	}
	blob, err := s.sealer.Seal(ctx, userID, plaintext)
	if err != nil {
		return fmt.Errorf("secrets: seal: %w", err)
	}
	const q = `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, name) DO UPDATE
		SET ciphertext = EXCLUDED.ciphertext,
		    kek_id     = EXCLUDED.kek_id,
		    updated_at = EXCLUDED.updated_at`
	_, err = s.db.ExecContext(ctx, q, userID, name, blob, s.sealer.KEKID(), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("secrets: insert: %w", err)
	}
	return nil
}

// Get reads + decrypts (userID, name). Returns ErrNotFound when
// the row doesn't exist; any other error is either a DB failure
// or crypto.ErrSealedBlob (from the sealer, wrapped).
func (s *Store) Get(ctx context.Context, userID uuid.UUID, name string) ([]byte, error) {
	if userID == uuid.Nil {
		return nil, errors.New("secrets: nil userID")
	}
	if name == "" {
		return nil, errors.New("secrets: empty name")
	}
	const q = `SELECT ciphertext FROM user_secrets WHERE user_id = $1 AND name = $2`
	var blob []byte
	err := s.db.QueryRowContext(ctx, q, userID, name).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: select: %w", err)
	}
	plaintext, err := s.sealer.Open(ctx, userID, blob)
	if err != nil {
		// Surface the sealer's ErrSealedBlob directly — it's
		// the signal an operator wants when diagnosing "I
		// rotated the KEK and forgot to add the old one to
		// the rotation list".
		return nil, err
	}
	return plaintext, nil
}

// Delete removes a secret. Idempotent: no error if the row is
// absent. Used on explicit logout / unlink and by the future
// GDPR-delete pipeline (though that will usually go through
// ON DELETE CASCADE from users).
func (s *Store) Delete(ctx context.Context, userID uuid.UUID, name string) error {
	if userID == uuid.Nil {
		return errors.New("secrets: nil userID")
	}
	if name == "" {
		return errors.New("secrets: empty name")
	}
	const q = `DELETE FROM user_secrets WHERE user_id = $1 AND name = $2`
	_, err := s.db.ExecContext(ctx, q, userID, name)
	if err != nil {
		return fmt.Errorf("secrets: delete: %w", err)
	}
	return nil
}
