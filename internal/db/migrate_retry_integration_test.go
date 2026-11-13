//go:build integration

package db_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

func openMigrated(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return ctx, d
}

// holdAccessShare keeps a transaction that has read table for hold, then releases it. ACCESS SHARE conflicts with the ACCESS EXCLUSIVE of an ALTER.
func holdAccessShare(t *testing.T, ctx context.Context, d *sql.DB, table string, hold time.Duration) {
	t.Helper()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT count(*) FROM "+table); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(hold)
		_ = tx.Rollback()
	}()
}

// A reader that outlasts the runner's 2 s lock_timeout makes the first attempts fail; the file runs again as a fresh transaction and succeeds once
// the reader is gone.
func TestMigrationRetriesALockTimeoutThenSucceeds(t *testing.T) {
	ctx, d := openMigrated(t)
	if _, err := d.ExecContext(ctx, `CREATE TABLE retry_t (id int)`); err != nil {
		t.Fatal(err)
	}
	defer db.SetLockRetryForTest(30*time.Second, 200*time.Millisecond, 400*time.Millisecond)()
	before := db.MigrateLockRetries()
	holdAccessShare(t, ctx, d, "retry_t", 5*time.Second)

	if err := db.RunMigrationRetryForTest(ctx, d, "9101_retry", "ALTER TABLE retry_t ADD COLUMN x int;"); err != nil {
		t.Fatalf("the migration did not succeed after the reader left: %v", err)
	}
	if got := db.MigrateLockRetries() - before; got < 1 {
		t.Errorf("retries = %d, want at least 1", got)
	}
	if applied(t, ctx, d, "9101_retry") != 1 {
		t.Error("the migration is not recorded")
	}
}

// The retry budget is bounded: a reader that never leaves fails the file with the cause, records nothing, and leaves the table unchanged.
func TestMigrationGivesUpAfterTheBudget(t *testing.T) {
	ctx, d := openMigrated(t)
	if _, err := d.ExecContext(ctx, `CREATE TABLE retry_t (id int)`); err != nil {
		t.Fatal(err)
	}
	defer db.SetLockRetryForTest(3*time.Second, 200*time.Millisecond, 400*time.Millisecond)()
	holdAccessShare(t, ctx, d, "retry_t", 30*time.Second)

	started := time.Now()
	err := db.RunMigrationRetryForTest(ctx, d, "9102_stuck", "ALTER TABLE retry_t ADD COLUMN x int;")
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("want a gave-up error, got %v", err)
	}
	if time.Since(started) > 12*time.Second {
		t.Errorf("took %s: the budget was not bounded", time.Since(started))
	}
	if applied(t, ctx, d, "9102_stuck") != 0 {
		t.Error("a failed migration was recorded")
	}
}

// A file that is not a lock problem is not retried.
func TestMigrationErrorIsNotRetried(t *testing.T) {
	ctx, d := openMigrated(t)
	defer db.SetLockRetryForTest(30*time.Second, 200*time.Millisecond, 400*time.Millisecond)()
	before := db.MigrateLockRetries()
	if err := db.RunMigrationRetryForTest(ctx, d, "9103_bad", "SELECT * FROM no_such_table;"); err == nil {
		t.Fatal("a bad statement succeeded")
	}
	if db.MigrateLockRetries() != before {
		t.Error("a non-lock error was retried")
	}
}

// The runner's limits are in force inside a file, and a file's own SET LOCAL replaces them.
func TestRunnerTimeoutsAndFileOverride(t *testing.T) {
	ctx, d := openMigrated(t)
	check := func(id, pre, want string) {
		t.Helper()
		body := pre + `DO $$ BEGIN IF current_setting('statement_timeout') <> '` + want + `' THEN RAISE EXCEPTION 'statement_timeout is %', current_setting('statement_timeout'); END IF;
IF current_setting('lock_timeout') <> '2s' THEN RAISE EXCEPTION 'lock_timeout is %', current_setting('lock_timeout'); END IF; END $$;`
		if err := db.RunMigrationForTest(ctx, d, id, body); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	check("9104_defaults", "", "1min")
	check("9105_override", "SET LOCAL statement_timeout = '30s';\n", "30s")
}

// Checksums: filled by Migrate, a changed row is counted and logged but never fails Migrate, and a NULL one is filled again.
func TestChecksumsFilledMismatchCountedNeverFatal(t *testing.T) {
	ctx, d := openMigrated(t) // Open migrates
	var nulls int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM migrations WHERE checksum IS NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("checksums missing after Migrate: %d (%v)", nulls, err)
	}
	if bad, err := db.CheckChecksums(ctx, d); err != nil || len(bad) != 0 || db.ChecksumMismatches() != 0 {
		t.Fatalf("clean database: %v %v %d", bad, err, db.ChecksumMismatches())
	}
	if _, err := d.ExecContext(ctx, `UPDATE migrations SET checksum = 'tampered' WHERE id = (SELECT min(id) FROM migrations)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE migrations SET checksum = NULL WHERE id = (SELECT max(id) FROM migrations)`); err != nil {
		t.Fatal(err)
	}
	bad, err := db.CheckChecksums(ctx, d)
	if err != nil || len(bad) != 1 || db.ChecksumMismatches() != 1 {
		t.Fatalf("one tampered row: %v %v %d", bad, err, db.ChecksumMismatches())
	}
	// The read-only form fills nothing; Migrate does, and still succeeds with the mismatch present.
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM migrations WHERE checksum IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("CheckChecksums must not write: nulls=%d %v", nulls, err)
	}
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatalf("a mismatch must not fail Migrate: %v", err)
	}
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM migrations WHERE checksum IS NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("Migrate did not fill the missing checksum: %d %v", nulls, err)
	}
	if db.ChecksumMismatches() != 1 {
		t.Errorf("mismatch gauge = %d, want 1", db.ChecksumMismatches())
	}
}
