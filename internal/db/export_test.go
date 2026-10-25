package db

import "time"

// Hooks for the external integration tests (migrate_job_integration_test.go).

const MigrationLockIDForTest = migrationLockID

func SetMigrationTimingsForTest(lockWait, lockPoll, schemaPollEvery time.Duration) (restore func()) {
	w, p, s := migrationLockWait, migrationLockPoll, schemaPoll
	migrationLockWait, migrationLockPoll, schemaPoll = lockWait, lockPoll, schemaPollEvery
	return func() { migrationLockWait, migrationLockPoll, schemaPoll = w, p, s }
}
