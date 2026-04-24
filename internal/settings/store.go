// Package settings persists app-level configuration (AI provider keys,
// charging cost, Rivian session) in the shared Postgres database.
//
// Storage shape: one row per (user_id, namespace) in the user_settings
// table, with the value column holding a JSONB object of string keys.
// The public Store API keeps the flat key/value illusion the Manager,
// charging and rivian helpers were built against — the namespace is
// derived from the key's first segment ("ai.*" → "ai", "charging.*" →
// "charging", "rivian.*" → "rivian").
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Store is a thin key/value wrapper around a Postgres connection pool
// scoped to a single user.
type Store struct {
	db     *sql.DB
	userID uuid.UUID
}

// OpenStore binds a pooled connection to a user_id. The pool is owned
// by the caller; Store does not close it.
func OpenStore(db *sql.DB, userID uuid.UUID) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("settings: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("settings: userID is zero")
	}
	return &Store{db: db, userID: userID}, nil
}

// Close is a no-op; the pool lifecycle is handled by main.
func (s *Store) Close() error { return nil }

// GetAll returns every stored key/value pair across every namespace,
// flattened. Keys keep their "ai.openai.model" / "charging.home_currency"
// shape so callers that were built against the old sqlite KV table
// don't need to know about namespaces.
func (s *Store) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT value FROM user_settings WHERE user_id = $1`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			// A malformed row shouldn't take the whole settings read
			// down; skip it and let the caller see the rest.
			continue
		}
		for k, v := range m {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// Set writes a single value. Empty string is a valid value ("cleared by
// operator"); callers that want to delete should use Delete instead.
// Writes are atomic at the (user_id, namespace) row level.
func (s *Store) Set(ctx context.Context, key, value string) error {
	ns := namespaceFor(key)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_settings (user_id, namespace, value)
		VALUES ($1, $2, jsonb_build_object($3::text, to_jsonb($4::text)))
		ON CONFLICT (user_id, namespace) DO UPDATE
			SET value = user_settings.value || jsonb_build_object($3::text, to_jsonb($4::text)),
			    updated_at = NOW()
	`, s.userID, ns, key, value)
	return err
}

// Delete removes a key. No-op if absent.
func (s *Store) Delete(ctx context.Context, key string) error {
	ns := namespaceFor(key)
	_, err := s.db.ExecContext(ctx, `
		UPDATE user_settings
		SET value = value - $3, updated_at = NOW()
		WHERE user_id = $1 AND namespace = $2
	`, s.userID, ns, key)
	return err
}

// namespaceFor maps a flat key to its JSONB namespace row. Unknown
// prefixes land in "misc" so the store never loses writes, though in
// practice every caller uses one of the three known prefixes.
func namespaceFor(key string) string {
	switch {
	case strings.HasPrefix(key, "ai."):
		return "ai"
	case strings.HasPrefix(key, "charging."):
		return "charging"
	case strings.HasPrefix(key, "rivian."):
		return "rivian"
	}
	return "misc"
}
