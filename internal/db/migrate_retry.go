package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Runner limits and the bounded lock retry (migrations v2, step 2; DA M2).
//
// Every transaction file starts with the runner's timeouts. The 2 s lock_timeout is the longest an ACCESS EXCLUSIVE request queues writes behind
// itself; 60 s statement_timeout stops a runaway statement; the idle limit stops a stuck session holding the migration's locks. A file that needs
// more says so with SET LOCAL, within the lint's caps.
//
// A lock not available (55P03) or a deadlock (40P01) is ordinary contention: the transaction rolled back and holds nothing, so the file runs again as
// a fresh transaction after a jittered pause. The whole file gets lockRetryBudget (30 s) of retrying; then it fails as before (the Job fails, old
// pods keep serving). Nobody has to delete a Job by hand for a busy second.
const runnerSettings = `SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '60s'; SET LOCAL idle_in_transaction_session_timeout = '30s'`

var (
	lockRetryBudget = 30 * time.Second
	lockRetryMin    = 1 * time.Second
	lockRetryMax    = 5 * time.Second
	lockRetries     atomic.Int64
)

// MigrateLockRetries is how many times this process has run a migration file again after a lock timeout or deadlock.
func MigrateLockRetries() int64 { return lockRetries.Load() }

func isLockContention(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "55P03" || pg.Code == "40P01")
}

// runWithLockRetry applies one migration, running it again on lock contention until the retry budget is spent.
func runWithLockRetry(ctx context.Context, db *sql.DB, m migration) error {
	started := time.Now()
	for attempt := 1; ; attempt++ {
		err := run(ctx, db, m)
		if err == nil || !isLockContention(err) {
			return err
		}
		pause := lockRetryMin
		if span := lockRetryMax - lockRetryMin; span > 0 {
			pause += time.Duration(rand.Int64N(int64(span)))
		}
		if time.Since(started)+pause > lockRetryBudget {
			return fmt.Errorf("gave up after %d attempts over %s: %w", attempt, time.Since(started).Round(time.Second), err)
		}
		lockRetries.Add(1)
		slog.Warn("migration waiting for a lock, trying again", "id", m.id, "attempt", attempt, "pause_ms", pause.Milliseconds())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}
