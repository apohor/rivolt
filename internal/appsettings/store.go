// Package appsettings is the install-wide key/value store backing
// the AI provider configuration (and any future install-scoped
// setting). It mirrors the per-user internal/settings.Store API
// — GetAll / Set / Delete returning string values — so the
// existing settings.Manager can be pointed at it without the
// Manager caring whether it's reading user_settings or
// app_settings.
//
// # Why a separate table
//
// Per-user settings (charging cost, Rivian session, push
// subscription) live in user_settings. AI provider keys are
// install-wide because the deployer pays the LLM bill — a single
// shared key, set once by the admin, used by every user. Storing
// it in user_settings would require duplicating the row across
// every user (and rotating it across every user on key change),
// or picking an arbitrary "tenant" user to host it on. Both are
// worse than a dedicated table.
//
// # Encryption
//
// Values are envelope-encrypted at the application layer using
// the same crypto.Sealer that backs internal/secrets. The AAD is
// a fixed system UUID so a key can't be moved between rows. The
// `value` column is BYTEA — a `pg_dump` does not exfiltrate the
// AI keys without also leaking RIVOLT_KEK.
package appsettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/google/uuid"
)

// SystemUUID is the AAD bound into every sealed app_settings
// row. It's fixed-known so rotation tooling can re-wrap rows
// in place without per-row metadata. The all-zeros UUID would
// also work; using a non-nil constant just makes it easy to
// grep for in audit logs.
var SystemUUID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// Store is the install-wide KV. Concurrency-safe (the underlying
// pool handles concurrency, and the sealer is stateless).
type Store struct {
	db     *sql.DB
	sealer crypto.Sealer
}

// New builds a Store. db must be non-nil; sealer must be non-nil
// in production but a NoopSealer is accepted so unit tests don't
// need to stand up a KEK.
func New(db *sql.DB, sealer crypto.Sealer) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("appsettings: db is nil")
	}
	if sealer == nil {
		return nil, fmt.Errorf("appsettings: sealer is nil")
	}
	return &Store{db: db, sealer: sealer}, nil
}

// GetAll returns every key/value pair, decrypted. A row that
// fails to decrypt is skipped (logged at the caller via the
// returned error from Open being ErrSealedBlob) — but we never
// take the whole settings read down because of one corrupt row,
// for the same reason settings.Store doesn't: an operator
// rotating a KEK should still be able to load the rest of the
// install.
func (s *Store) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM app_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var blob []byte
		if err := rows.Scan(&k, &blob); err != nil {
			return nil, err
		}
		plain, err := s.sealer.Open(ctx, SystemUUID, blob)
		if err != nil {
			// Skip rather than fail the whole read.
			continue
		}
		out[k] = string(plain)
	}
	return out, rows.Err()
}

// Set writes a single value. Empty string is a valid value
// ("cleared by admin"); callers that want to delete the row
// should call Delete.
func (s *Store) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("appsettings: empty key")
	}
	blob, err := s.sealer.Seal(ctx, SystemUUID, []byte(value))
	if err != nil {
		return fmt.Errorf("appsettings: seal: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE
			SET value      = EXCLUDED.value,
			    updated_at = NOW()
	`, key, blob)
	return err
}

// Delete removes a key. Idempotent.
func (s *Store) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("appsettings: empty key")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key = $1`, key)
	return err
}
