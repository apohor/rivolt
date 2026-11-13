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

// MigrationFile is one embedded migration, for the probe (migrate_probe_integration_test.go).
type MigrationFile struct{ ID, SQL string }

// MigrationFilesForTest lists the embedded migrations in apply order.
func MigrationFilesForTest() ([]MigrationFile, error) {
	steps, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	out := make([]MigrationFile, len(steps))
	for i, m := range steps {
		out[i] = MigrationFile{ID: m.id, SQL: m.sql}
	}
	return out, nil
}
