package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
)

// grantLockID serializes the role grants across replicas. Two sessions that GRANT (or ALTER DEFAULT
// PRIVILEGES, or CREATE OR REPLACE a function) on the same object at the same moment can fail with
// "tuple concurrently updated", and at a rollout every pod applies them at boot together. Each
// replica still applies its own list (a pod of a new version knows tables an old one does not), one
// at a time, so the lock waits instead of skipping.
const grantLockID int64 = 0x72765f6772616e // "rv_gran"

// grantExec is what the grant statements run on: the connection holding the lock.
type grantExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// withGrantLock runs fn on one connection that holds the grant lock, waiting for it (bounded by
// ctx). If the unlock fails the connection is discarded, so a pooled session never keeps the lock.
func withGrantLock(ctx context.Context, d *sql.DB, fn func(q grantExec) error) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("grant lock: %w", err)
	}
	defer func() {
		var unlocked bool
		if err := conn.QueryRowContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, grantLockID).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, grantLockID); err != nil {
		return fmt.Errorf("grant lock: %w", err)
	}
	return fn(conn)
}
