package db

import (
	"database/sql"
	"testing"
)

// newFakeDB returns a non-nil *sql.DB that is never used to query (pgx
// opens connections lazily), for tests of code that only checks the handle.
func newFakeDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
