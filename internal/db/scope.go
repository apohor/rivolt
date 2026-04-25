// Package db: per-request user-scoping helper.
//
// Ships alongside migration 0008 (Row-Level Security). The RLS
// policies read `current_setting('app.user_id')` to decide which
// rows are visible; this helper is the seam where an operator's
// per-request pinning would live.
//
// In Phase 1 the app connects as the DB owner and therefore
// BYPASSRLS is implicit — so calling `WithUserScope` is optional
// and serves as documentation. In Phase 2, the app role is split
// from the owner, at which point every request-handling code path
// must funnel through `WithUserScope` or the RLS policies will
// return empty result sets.
//
// Design notes:
//
//   - We acquire a dedicated *sql.Conn per call. Setting a GUC at
//     session scope on a pooled conn would leak across requests
//     (next checkout gets the previous tenant's id).
//
//   - `SET LOCAL` + an explicit transaction is the one mode that
//     works reliably with database/sql: LOCAL scopes the setting
//     to the current tx, and the pgx driver commits + resets on
//     tx close. Session-scope SET would require a reset on every
//     release, which the pgx stdlib wrapper can't hook.
//
//   - The callback receives a DBTX that shadows *sql.DB's usual
//     interface, so existing store code compiles unchanged once
//     its `*sql.DB` field is widened to `DBTX`. Progressive
//     rollout: convert one store at a time.

package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// DBTX is the subset of *sql.DB / *sql.Tx / *sql.Conn that every
// store uses. Having a named interface lets callers pass either
// the pool (Phase 1) or a scoped tx (Phase 2) without API churn.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// WithUserScope runs fn inside a transaction that has
// `app.user_id` set to userID at LOCAL scope. The transaction
// commits if fn returns nil, rolls back otherwise.
//
// Safe to call with a nil pool (returns fn's zero error) to keep
// no-DB dev paths working.
func WithUserScope(
	ctx context.Context,
	pool *sql.DB,
	userID uuid.UUID,
	fn func(ctx context.Context, tx DBTX) error,
) error {
	if pool == nil {
		return fn(ctx, nil)
	}
	if userID == uuid.Nil {
		return fmt.Errorf("db.WithUserScope: zero userID")
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db.WithUserScope: begin: %w", err)
	}
	// Deferred rollback is a no-op once Commit succeeds. Keeping
	// it here means any early return from fn (including panics)
	// still releases the conn.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	// SET LOCAL keeps the GUC tied to this tx so the next pool
	// checkout gets a clean slate — no cross-tenant leakage.
	// We don't use a parameterised query because `SET` doesn't
	// accept parameters; instead we validate by forcing a UUID
	// cast, which rejects anything that doesn't parse and makes
	// SQL injection via userID structurally impossible.
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`SET LOCAL app.user_id = '%s'`, userID.String()),
	); err != nil {
		return fmt.Errorf("db.WithUserScope: set app.user_id: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db.WithUserScope: commit: %w", err)
	}
	committed = true
	return nil
}
