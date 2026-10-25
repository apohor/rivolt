package db

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestMigrateOnStart(t *testing.T) {
	for v, want := range map[string]bool{"": true, "true": true, "TRUE": true, "1": true, "yes": true, "false": false, "False": false, " 0 ": false, "no": false, "off": false} {
		if got := MigrateOnStart(func(string) string { return v }); got != want {
			t.Errorf("RIVOLT_MIGRATE_ON_START=%q: got %v, want %v", v, got, want)
		}
	}
}

// The refusal tells the person the fix in one line (DA ruling): the command, and the setting for a single-node install.
func TestSchemaBehindMessageNamesTheFix(t *testing.T) {
	msg := ErrSchemaBehind.Error()
	for _, want := range []string{`run "rivolt migrate"`, "RIVOLT_MIGRATE_ON_START=true", "single-node"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "\n") || !errors.Is(ErrSchemaBehind, ErrSchemaBehind) {
		t.Errorf("message must be one line: %q", msg)
	}
}

// --- Destructive migrations carry a marker and the devils-advocate's ack ---------------------------------------------------
//
// A rollback is the old image on the newer schema, so a migration must not remove what the previous release still reads, and a
// migration that deletes user data cannot be undone except from a backup. Any NEW migration that drops, renames, tightens or
// deletes must say so with a "-- contract: <why no running version needs this>" line; review (and the devils-advocate's ack in
// that PR) is the gate, this test only makes the statement impossible to forget. The 90 migrations that exist are grandfathered.

const contractFrom = 91

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlString       = regexp.MustCompile(`'(?:[^']|'')*'`)
	contractMarker  = regexp.MustCompile(`(?m)^\s*--\s*contract:\s*\S`)
	destructiveDDL  = regexp.MustCompile(`(?is)\bdrop\s+(table|column|schema|type|constraint)\b|\brename\s+(to|column|constraint)\b|\balter\s+table\b[^;]*\brename\b|\bset\s+not\s+null\b|\balter\s+column\b[^;]*\b(set\s+data\s+)?type\b`)
	deleteOrTrunc   = regexp.MustCompile(`(?is)\bdelete\s+from\b|\btruncate\b`)
	updateStatement = regexp.MustCompile(`(?is)^\s*update\b`)
	whereClause     = regexp.MustCompile(`(?is)\bwhere\b`)
)

// destructiveReason says why sql needs a contract marker, or "" if it does not. Comments and string literals are ignored.
func destructiveReason(sql string) string {
	s := sqlBlockComment.ReplaceAllString(sql, " ")
	s = sqlLineComment.ReplaceAllString(s, " ")
	s = sqlString.ReplaceAllString(s, "''")
	if destructiveDDL.MatchString(s) {
		return "drops, renames or tightens schema"
	}
	if deleteOrTrunc.MatchString(s) {
		return "deletes rows"
	}
	for _, stmt := range strings.Split(s, ";") {
		if updateStatement.MatchString(stmt) && !whereClause.MatchString(stmt) {
			return "updates every row (no WHERE)"
		}
	}
	return ""
}

func TestDestructiveReason(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want bool
	}{
		{"ALTER TABLE t ADD COLUMN x int", false},
		{"CREATE TABLE t (id int)", false},
		{"CREATE INDEX i ON t (x)", false},
		{"ALTER TABLE t DROP COLUMN x", true},
		{"drop table t", true},
		{"ALTER TABLE t RENAME COLUMN a TO b", true},
		{"ALTER TABLE t RENAME TO u", true},
		{"ALTER TABLE t ALTER COLUMN x SET NOT NULL", true},
		{"ALTER TABLE t ALTER COLUMN x TYPE bigint", true},
		{"ALTER TABLE t ALTER COLUMN x SET DEFAULT 1", false},
		{"DELETE FROM t WHERE id = 1", true},
		{"TRUNCATE t", true},
		{"UPDATE t SET x = 1", true},
		{"UPDATE t SET x = 1 WHERE y IS NULL", false},
		{"INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO UPDATE SET a = 2", false},
		{"-- drop table t\nSELECT 1", false},
		{"INSERT INTO t (a) VALUES ('delete from x; drop table y')", false},
		{"/* truncate t */ SELECT 1", false},
	} {
		if got := destructiveReason(c.sql) != ""; got != c.want {
			t.Errorf("%q: destructive=%v, want %v", c.sql, got, c.want)
		}
	}
}

func TestNewDestructiveMigrationsCarryAContractMarker(t *testing.T) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil || n < contractFrom {
			continue
		}
		body, err := fs.ReadFile(migrationFS, name)
		if err != nil {
			t.Fatal(err)
		}
		if why := destructiveReason(string(body)); why != "" && !contractMarker.Match(body) {
			t.Errorf("%s %s: add a line \"-- contract: <why no running version needs what this removes>\" and get the devils-advocate's ack in the PR", base, why)
		}
	}
}

// --- Only the migrate job and the server's startup may change the schema -------------------------------------------------------
//
// The repair and maintenance commands connect with OpenNoMigrate, so a CLI built from a newer tree and pointed at a database
// never applies that tree's migrations as a side effect (the hazard behind #168). This keeps it so: db.Open, db.Migrate and
// OpenWithRetry may be called from the places below and nowhere else in non-test code.

var schemaChangers = regexp.MustCompile(`\bdb\.(Open|Migrate|OpenWithRetry)\(`)

var mayChangeSchema = map[string]string{
	"cmd/rivolt/main.go":    "the server's startup (db.OpenWithRetry: migrates, or checks the schema when a job migrates)",
	"cmd/rivolt/migrate.go": "`rivolt migrate`, the migration job",
}

func TestOnlyTheServerAndTheMigrateJobChangeTheSchema(t *testing.T) {
	root := filepath.Join("..", "..")
	used := map[string]bool{}
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", "web", "ios", "internal/db", "internal/testpg", "internal/web":
				return filepath.SkipDir
			}
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		if schemaChangers.Match(b) {
			if _, ok := mayChangeSchema[rel]; ok {
				used[rel] = true
			} else {
				t.Errorf("%s calls db.Open, db.Migrate or db.OpenWithRetry: use db.OpenNoMigrate (only %v may change the schema)", rel, keys(mayChangeSchema))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d Go files: the walk is not seeing the tree", scanned)
	}
	for rel, why := range mayChangeSchema {
		if !used[rel] {
			t.Errorf("%s (%s) no longer calls the schema-changing functions: delete it from mayChangeSchema", rel, why)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
