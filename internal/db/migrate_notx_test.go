package db

import (
	"strings"
	"testing"
)

func TestNoTxDirectiveAndStatementCheck(t *testing.T) {
	good := "-- migrate:no-transaction\n-- why: the table is big\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS drives_x_idx ON drives (user_id, started_at);\n"
	if !isNoTx(good) {
		t.Fatal("directive not seen")
	}
	if name, err := noTxIndexName(good); err != nil || name != "drives_x_idx" {
		t.Fatalf("good migration: %q %v", name, err)
	}
	if isNoTx("-- migrate:no-transactions\nSELECT 1;") || isNoTx("SELECT 1;\n-- migrate:no-transaction") {
		t.Error("directive must be the first line, exactly")
	}
	for name, bad := range map[string]string{
		"two statements":        "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a ON t (x);\nCREATE INDEX CONCURRENTLY IF NOT EXISTS b ON t (y);",
		"no IF NOT EXISTS":      "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY a ON t (x);",
		"not concurrently":      "-- migrate:no-transaction\nCREATE INDEX IF NOT EXISTS a ON t (x);",
		"something else":        "-- migrate:no-transaction\nALTER TABLE t ADD COLUMN x int;",
		"no semicolon":          "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a ON t (x)",
		"a quote":               "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a ON t (x) WHERE s = 'a;b';",
		"a dollar quote":        "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a ON t (x) WHERE s = $$x$$;",
		"an INSERT":             "-- migrate:no-transaction\nINSERT INTO drives (id) VALUES (1);",
		"an UPDATE":             "-- migrate:no-transaction\nUPDATE charges SET energy_added_kwh = 0;",
		"a DELETE":              "-- migrate:no-transaction\nDELETE FROM vehicle_state;",
		"a DROP":                "-- migrate:no-transaction\nDROP INDEX CONCURRENTLY IF EXISTS a;",
		"an index then a write": "-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a ON t (x); DELETE FROM t;",
		"empty after comment":   "-- migrate:no-transaction\n-- nothing\n",
	} {
		if _, err := noTxIndexName(bad); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: empty error", name)
		}
	}
}
