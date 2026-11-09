//go:build integration

package db_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

func indexValid(t *testing.T, ctx context.Context, d *sql.DB, name string) (exists, valid bool) {
	t.Helper()
	var v sql.NullBool
	err := d.QueryRowContext(ctx, `SELECT i.indisvalid FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid WHERE c.relname = $1`, name).Scan(&v)
	if err == sql.ErrNoRows {
		return false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true, v.Bool
}

func applied(t *testing.T, ctx context.Context, d *sql.DB, id string) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM migrations WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A no-transaction migration builds the index concurrently and records itself; running it again changes nothing; a multi-statement file and a
// non-index statement are refused and record nothing.
func TestNoTransactionMigration(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `CREATE TABLE notx_t (id int, k int)`); err != nil {
		t.Fatal(err)
	}
	body := "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS notx_k_idx ON notx_t (k);\n"
	if err := db.RunMigrationForTest(ctx, d, "9001_notx", body); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if ex, ok := indexValid(t, ctx, d, "notx_k_idx"); !ex || !ok || applied(t, ctx, d, "9001_notx") != 1 {
		t.Fatalf("after the run: exists=%v valid=%v recorded=%d", ex, ok, applied(t, ctx, d, "9001_notx"))
	}
	repairs := db.IndexRepairs()
	if err := db.RunMigrationForTest(ctx, d, "9001_notx", body); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if applied(t, ctx, d, "9001_notx") != 1 || db.IndexRepairs() != repairs {
		t.Errorf("a re-run recorded again or repaired something")
	}

	for name, bad := range map[string]string{
		"two statements": "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a_idx ON notx_t (id);\nCREATE INDEX CONCURRENTLY IF NOT EXISTS b_idx ON notx_t (k);",
		"an ALTER":       "-- migrate:no-transaction\nALTER TABLE notx_t ADD COLUMN z int;",
	} {
		if err := db.RunMigrationForTest(ctx, d, "9002_bad", bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if applied(t, ctx, d, "9002_bad") != 0 {
		t.Error("a refused migration was recorded")
	}
	if ex, _ := indexValid(t, ctx, d, "a_idx"); ex {
		t.Error("a refused file still ran its first statement")
	}
}

// A failed CREATE UNIQUE INDEX CONCURRENTLY leaves an INVALID index; IF NOT EXISTS would skip it for ever. The runner drops it, counts it, and builds it
// again once the data allows.
func TestNoTransactionMigrationRepairsAnInvalidLeftover(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `CREATE TABLE notx_u (id int, k int); INSERT INTO notx_u VALUES (1, 7), (2, 7)`); err != nil {
		t.Fatal(err)
	}
	body := "-- migrate:no-transaction\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS notx_u_idx ON notx_u (k);\n"
	if err := db.RunMigrationForTest(ctx, d, "9003_notx_u", body); err == nil {
		t.Fatal("a unique index over duplicates must fail")
	}
	if ex, ok := indexValid(t, ctx, d, "notx_u_idx"); !ex || ok {
		t.Fatalf("expected an invalid leftover, got exists=%v valid=%v", ex, ok)
	}
	if applied(t, ctx, d, "9003_notx_u") != 0 {
		t.Fatal("a failed migration was recorded")
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM notx_u WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	before := db.IndexRepairs()
	if err := db.RunMigrationForTest(ctx, d, "9003_notx_u", body); err != nil {
		t.Fatalf("after fixing the data: %v", err)
	}
	if ex, ok := indexValid(t, ctx, d, "notx_u_idx"); !ex || !ok {
		t.Errorf("the index is not valid after the retry: exists=%v valid=%v", ex, ok)
	}
	if db.IndexRepairs() != before+1 || applied(t, ctx, d, "9003_notx_u") != 1 {
		t.Errorf("repairs %d -> %d, recorded %d; want one repair and one record", before, db.IndexRepairs(), applied(t, ctx, d, "9003_notx_u"))
	}
}
