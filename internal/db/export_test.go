package db

import (
	"context"
	"database/sql"
	"time"
)

// Hooks for the external integration tests (migrate_job_integration_test.go).

const MigrationLockIDForTest = migrationLockID

func SetMigrationTimingsForTest(lockWait, lockPoll, schemaPollEvery time.Duration) (restore func()) {
	w, p, s := migrationLockWait, migrationLockPoll, schemaPoll
	migrationLockWait, migrationLockPoll, schemaPoll = lockWait, lockPoll, schemaPollEvery
	return func() { migrationLockWait, migrationLockPoll, schemaPoll = w, p, s }
}

// RunMigrationForTest applies one migration the way Migrate does (transactional, or the no-transaction form).
func RunMigrationForTest(ctx context.Context, d *sql.DB, id, body string) error {
	return run(ctx, d, migration{id: id, sql: body})
}

// RunMigrationRetryForTest is RunMigrationForTest with the bounded lock retry Migrate uses.
func RunMigrationRetryForTest(ctx context.Context, d *sql.DB, id, body string) error {
	return runWithLockRetry(ctx, d, migration{id: id, sql: body})
}

func SetLockRetryForTest(budget, min, max time.Duration) (restore func()) {
	b, lo, hi := lockRetryBudget, lockRetryMin, lockRetryMax
	lockRetryBudget, lockRetryMin, lockRetryMax = budget, min, max
	return func() { lockRetryBudget, lockRetryMin, lockRetryMax = b, lo, hi }
}
