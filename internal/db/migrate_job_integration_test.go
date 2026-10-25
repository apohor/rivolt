//go:build integration

package db_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// A database one step behind this binary is refused by the server's check, `migrate` brings it current, and a database AHEAD of the
// binary (a rolled-back image) is accepted.
func TestRequireSchemaBehindCurrentAndAhead(t *testing.T) {
	ctx := context.Background()
	d, err := db.OpenNoMigrate(ctx, testpg.Fresh(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	st, err := db.CheckSchema(ctx, d)
	// A fresh database has everything pending. The count is whatever this tree ships (the files are not numbered gaplessly), so
	// compare against the embedded list, not a constant.
	latest, lerr := db.LatestEmbedded()
	if err != nil || lerr != nil || len(st.Pending) < 50 || len(st.Unknown) != 0 || st.Pending[len(st.Pending)-1] != latest {
		t.Fatalf("fresh database: pending=%d unknown=%d latest=%q err=%v/%v", len(st.Pending), len(st.Unknown), latest, err, lerr)
	}
	err = db.RequireSchema(ctx, d, 0, quiet)
	if !errors.Is(err, db.ErrSchemaBehind) {
		t.Fatalf("a fresh database must be refused with ErrSchemaBehind, got %v", err)
	}

	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if st, _ = db.CheckSchema(ctx, d); len(st.Pending) != 0 {
		t.Fatalf("pending after migrate: %v", st.Pending)
	}
	if err := db.RequireSchema(ctx, d, 0, quiet); err != nil {
		t.Fatalf("a current database must pass: %v", err)
	}

	// Ahead: a migration this binary does not ship.
	if _, err := d.ExecContext(ctx, `INSERT INTO migrations (id, applied_at, note) VALUES ('9999_from_a_newer_release', 1, '')`); err != nil {
		t.Fatal(err)
	}
	if err := db.RequireSchema(ctx, d, 0, quiet); err != nil {
		t.Fatalf("a database ahead of the binary must pass: %v", err)
	}
	if st, _ = db.CheckSchema(ctx, d); len(st.Unknown) != 1 || st.Unknown[0] != "9999_from_a_newer_release" {
		t.Fatalf("unknown: %v", st.Unknown)
	}
}

// A server that starts while the job is still running waits for it instead of failing.
func TestRequireSchemaWaitsForTheJob(t *testing.T) {
	ctx := context.Background()
	defer db.SetMigrationTimingsForTest(time.Minute, 50*time.Millisecond, 100*time.Millisecond)()
	dsn := testpg.Fresh(ctx, t)
	server, err := db.OpenNoMigrate(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	job, err := db.OpenNoMigrate(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()

	done := make(chan error, 1)
	go func() {
		time.Sleep(500 * time.Millisecond)
		done <- db.Migrate(ctx, job)
	}()
	if err := db.RequireSchema(ctx, server, 2*time.Minute, quiet); err != nil {
		t.Fatalf("the server should have waited for the job: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// One migrator at a time: a second one waits for the lock, and gives up with the holder's pid when it is held too long.
func TestMigrateLockWaitsAndTimesOut(t *testing.T) {
	ctx := context.Background()
	defer db.SetMigrationTimingsForTest(400*time.Millisecond, 50*time.Millisecond, time.Second)()
	d, err := db.OpenNoMigrate(ctx, testpg.Fresh(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	before, err := db.CheckSchema(ctx, d)
	if err != nil || len(before.Pending) < 50 {
		t.Fatalf("fresh database: pending=%d err=%v", len(before.Pending), err)
	}
	holder, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", db.MigrationLockIDForTest); err != nil {
		t.Fatal(err)
	}

	err = db.Migrate(ctx, d)
	if err == nil || !strings.Contains(err.Error(), "another migrator") || !strings.Contains(err.Error(), "backend pid") {
		t.Fatalf("a held lock should time out naming the holder, got %v", err)
	}
	if after, _ := db.CheckSchema(ctx, d); len(after.Pending) != len(before.Pending) {
		t.Fatalf("a migrator that never got the lock must have changed nothing: pending %d before, %d after", len(before.Pending), len(after.Pending))
	}

	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", db.MigrationLockIDForTest); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatalf("with the lock free it should migrate: %v", err)
	}
}
