// Package singleton runs a periodic job on one replica at a time. The replica that takes the job's
// Postgres advisory lock runs it; one that finds the lock held skips that tick (counted and logged
// at debug), and the next tick catches up, since every job that uses this is idempotent. The lock is
// session-level on a dedicated connection, so a pod that dies releases it with its connection.
package singleton

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"
)

// Outcomes, as the metric labels them.
const (
	Ran     = "ran"
	Skipped = "skipped" // another replica held the lock
	Errored = "error"   // the lock could not be tried (no connection, database error)
)

// Key names a job's lock: Name is hashed by Postgres; ID is used as is, for a job whose lock existed before this package
// (its old pods must keep excluding the new ones during a rollout). Name wins when both are set.
type Key struct {
	Name string
	ID   int64
}

var observer atomic.Pointer[func(job, outcome string)]

// SetObserver tells f every attempt's outcome (job, outcome), for rivolt_singleton_runs_total. Set once at boot.
func SetObserver(f func(job, outcome string)) { observer.Store(&f) }

func observe(job, outcome string) {
	if f := observer.Load(); f != nil {
		(*f)(job, outcome)
	}
}

// Acquire takes job's lock without waiting. When it returns ok, the caller holds the lock until it calls release (on a connection kept
// for the purpose; a pod that dies releases it with the connection). A held lock is (nil, false, nil), counted as skipped; a fault
// trying it is (nil, false, err).
func Acquire(ctx context.Context, d *sql.DB, job string, key Key) (release func(), ok bool, err error) {
	if d == nil {
		return nil, false, nil
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		observe(job, Errored)
		return nil, false, err
	}
	try, unlock := `SELECT pg_try_advisory_lock($1)`, `SELECT pg_advisory_unlock($1)`
	var arg any = key.ID
	if key.Name != "" {
		try, unlock = `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`
		arg = key.Name
	}
	var locked bool
	if err := conn.QueryRowContext(ctx, try, arg).Scan(&locked); err != nil {
		_ = conn.Close()
		observe(job, Errored)
		return nil, false, err
	}
	if !locked {
		_ = conn.Close()
		observe(job, Skipped)
		slog.DebugContext(ctx, "singleton job skipped: another replica is running it", "job", job)
		return nil, false, nil
	}
	observe(job, Ran)
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), unlock, arg)
		_ = conn.Close()
	}, true, nil
}

// Run is Acquire around fn: it runs fn under job's lock and reports whether it ran. The lock is released when fn returns, however it ends.
func Run(ctx context.Context, d *sql.DB, job string, key Key, fn func(ctx context.Context) error) (bool, error) {
	release, ok, err := Acquire(ctx, d, job, key)
	if err != nil || !ok {
		return false, err
	}
	defer release()
	return true, fn(ctx)
}
