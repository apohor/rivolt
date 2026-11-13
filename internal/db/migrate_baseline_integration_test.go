//go:build integration

package db_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// migrationStamps is every migrations row with the transaction that wrote it: two equal maps mean nothing was written between them.
func migrationStamps(t *testing.T, ctx context.Context, d *sql.DB) map[string]string {
	t.Helper()
	rows, err := d.QueryContext(ctx, `SELECT id, xmin::text || '/' || coalesce(checksum, '-') || '/' || note FROM migrations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, stamp string
		if err := rows.Scan(&id, &stamp); err != nil {
			t.Fatal(err)
		}
		out[id] = stamp
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// An empty database gets the baseline and every id it covers; a second run writes nothing; the files newer than the baseline still run.
func TestBaselineOnAnEmptyDatabaseThenNoWrites(t *testing.T) {
	ctx := context.Background()
	d, err := db.OpenNoMigrate(ctx, testpg.Fresh(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	st, err := db.CheckSchema(ctx, d)
	if err != nil || len(st.Pending) != 0 || len(st.Unknown) != 0 {
		t.Fatalf("after migrate: pending=%v unknown=%v err=%v", st.Pending, st.Unknown, err)
	}
	var fromBaseline, fromFiles int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE note LIKE 'baseline of %'), count(*) FILTER (WHERE note NOT LIKE 'baseline of %') FROM migrations`).
		Scan(&fromBaseline, &fromFiles); err != nil {
		t.Fatal(err)
	}
	if fromBaseline < 100 {
		t.Fatalf("%d ids recorded by the baseline", fromBaseline)
	}
	t.Logf("recorded by the baseline: %d, run from files: %d", fromBaseline, fromFiles)

	// The dump sets an empty search_path on its connection; no pooled connection may keep it.
	for i := 0; i < 5; i++ {
		var sp string
		if err := d.QueryRowContext(ctx, `SHOW search_path`).Scan(&sp); err != nil || !strings.Contains(sp, "public") {
			t.Fatalf("search_path %q (%v)", sp, err)
		}
	}
	// And the seed rows came with it.
	var flags int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM flags WHERE name IN ('ai_call_cap', 'rivian_upstream_paused', 'signup_cap')`).Scan(&flags); err != nil || flags != 3 {
		t.Fatalf("seeded flags: %d (%v)", flags, err)
	}

	before := migrationStamps(t, ctx, d)
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	after := migrationStamps(t, ctx, d)
	if len(before) != len(after) {
		t.Fatalf("rows %d -> %d", len(before), len(after))
	}
	for id, s := range before {
		if after[id] != s {
			t.Fatalf("%s was written by the second run: %s -> %s", id, s, after[id])
		}
	}
}

// A database that has some of what the baseline covers is refused, naming the release to upgrade to, and is not touched.
func TestBaselineRefusesAPartialDatabase(t *testing.T) {
	ctx := context.Background()
	d, err := db.OpenNoMigrate(ctx, testpg.Fresh(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM migrations WHERE id IN (SELECT id FROM migrations ORDER BY id DESC LIMIT 1 OFFSET 3)`); err != nil {
		t.Fatal(err)
	}
	before := migrationStamps(t, ctx, d)
	err = db.Migrate(ctx, d)
	if !errors.Is(err, db.ErrBaselinePartial) || !strings.Contains(err.Error(), "upgrade to release v") {
		t.Fatalf("want ErrBaselinePartial naming the release, got %v", err)
	}
	after := migrationStamps(t, ctx, d)
	if len(before) != len(after) {
		t.Fatalf("a refused run changed the table: %d -> %d rows", len(before), len(after))
	}
	for id, s := range before {
		if after[id] != s {
			t.Fatalf("%s changed", id)
		}
	}
	if st, _ := db.CheckSchema(ctx, d); len(st.Pending) != 1 {
		t.Fatalf("pending: %v", st.Pending)
	}
}
