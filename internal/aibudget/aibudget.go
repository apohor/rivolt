// Package aibudget enforces a per-user daily cap on LLM-backed calls
// (trip advice, drive efficiency). It is a cost backstop against a
// single account hammering the expensive AI endpoints, not a security
// control — the operator-tunable limit lives in the flags table
// (flags.AICallCapName) and a non-positive limit disables the cap.
//
// The counter lives in the ai_call_usage table (migration 0033), one
// row per (user_id, day). TryConsume increments it atomically and
// reports whether the call is within budget, so two pods racing the
// same user can't both slip a request past the cap.
package aibudget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Store consumes per-user daily AI-call budget against the shared
// Postgres pool. The pool is owned by the caller.
type Store struct {
	db *sql.DB
}

// New wires a Store to an already-open pool. A nil pool yields a nil
// Store; the gate treats a nil Store as "no cap" so no-DB dev paths
// keep working.
func New(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// TryConsume atomically charges one call against today's budget for
// uid and reports whether it was allowed plus the resulting day count.
//
// dailyLimit <= 0 means "no cap": the call is allowed and nothing is
// written. Otherwise a single upsert either inserts the first call of
// the day or increments an existing row only while it is still under
// the limit; when the row is already at the limit the conditional
// UPDATE matches nothing and RETURNING yields no row, which we map to
// (allowed=false). The increment is on accept, so an attempt that
// later fails inside the LLM still counts — the conservative choice
// for a runaway-cost guard.
func (s *Store) TryConsume(ctx context.Context, uid uuid.UUID, dailyLimit int) (allowed bool, used int, err error) {
	if s == nil || s.db == nil || dailyLimit <= 0 {
		return true, 0, nil
	}
	if uid == uuid.Nil {
		return false, 0, fmt.Errorf("aibudget: zero userID")
	}
	const q = `
		INSERT INTO ai_call_usage (user_id, day, calls)
		VALUES ($1, CURRENT_DATE, 1)
		ON CONFLICT (user_id, day) DO UPDATE
			SET calls = ai_call_usage.calls + 1
			WHERE ai_call_usage.calls < $2
		RETURNING calls`
	err = s.db.QueryRowContext(ctx, q, uid, dailyLimit).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		// Conflict whose WHERE excluded the row: already at the cap.
		return false, dailyLimit, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("aibudget: consume: %w", err)
	}
	return true, used, nil
}
