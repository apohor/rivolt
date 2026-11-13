package db

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Migrations v2, step 2: the lint holes and the compatibility lints, from lintV2From on (earlier files are history; TestV2LintsOverHistory logs what
// they would have said).
//
//  1. ADD CONSTRAINT ... FOREIGN KEY or CHECK without NOT VALID scans the whole table under a write-blocking lock. Add it NOT VALID and VALIDATE
//     CONSTRAINT in a later file (0112 and 0115 did).
//  2. ADD CONSTRAINT ... UNIQUE or PRIMARY KEY (and an inline UNIQUE/PRIMARY KEY column) builds a non-concurrent index under a lock. Build the index
//     with CREATE UNIQUE INDEX CONCURRENTLY first, then ADD CONSTRAINT ... USING INDEX.
//  3. The previous release keeps running on the new schema (a rollout, a canary, a rollback, ingest/admin/demo on their own tags), so whatever
//     makes its writes fail needs a "-- contract: <why no running version needs this>" line, like a drop does: ADD COLUMN ... NOT NULL with no
//     DEFAULT, SET NOT NULL, and a new CHECK, UNIQUE or PRIMARY KEY on an existing table.
//  4. lock_timeout above 2 s (DA M2: the longest an ACCESS EXCLUSIVE request may queue writes) and statement_timeout above 60 s or 0 (no limit) need
//     "-- da-ack: rewrite <reason>". The runner already sets 2 s and 60 s; a file asks for more only with the ack.
const lintV2From = 120

const maxLockTimeoutV2 = 2 * time.Second

var (
	reAddConstraintFKCheck = regexp.MustCompile(`(?is)\badd\s+(?:constraint\s+\S+\s+)?(foreign\s+key|check)\b`)
	reAddConstraintIndexed = regexp.MustCompile(`(?is)\badd\s+(?:constraint\s+\S+\s+)?(unique|primary\s+key)\b`)
	reNotValid             = regexp.MustCompile(`(?is)\bnot\s+valid\b`)
	reUsingIndex           = regexp.MustCompile(`(?is)\busing\s+index\b`)
	reInlineIndexed        = regexp.MustCompile(`(?is)\b(unique|primary\s+key)\b`)
	reInlineCheck          = regexp.MustCompile(`(?is)\bcheck\s*\(`)
	reNotNull              = regexp.MustCompile(`(?is)\bnot\s+null\b`)
	reHasDefault           = regexp.MustCompile(`(?is)\bdefault\b|\bgenerated\b|\bserial\b`)
	reCreateUniqueIndex    = regexp.MustCompile(`(?is)^create\s+unique\s+index\s+(?:concurrently\s+)?(?:if\s+not\s+exists\s+)?\S*\s*on\s+(?:only\s+)?(\S+)`)
	reStatementTimeoutSet  = regexp.MustCompile(`(?is)^set\s+(?:local\s+)?statement_timeout\s*(?:=|to)\s*'?\s*(\d+)\s*(ms|s|min|h|d)?\s*'?\s*$`)
	reLockTimeoutSetAny    = regexp.MustCompile(`(?is)^set\s+(?:local\s+)?lock_timeout\s*(?:=|to)\s*'?\s*(\d+)\s*(ms|s|min|h|d)?\s*'?\s*$`)
)

func timeoutValue(n, unit string) time.Duration {
	units := map[string]time.Duration{"": time.Millisecond, "ms": time.Millisecond, "s": time.Second, "min": time.Minute, "h": time.Hour, "d": 24 * time.Hour}
	var v int64
	for _, c := range n {
		v = v*10 + int64(c-'0')
	}
	return time.Duration(v) * units[strings.ToLower(unit)]
}

// v2LockFindings is lint 1 and 2, and 4's timeouts; a file with the rewrite ack skips the timeout part only.
func v2LockFindings(src string) []string {
	var bad []string
	created := map[string]bool{}
	for _, st := range sqlStatements(src) {
		t := st.text
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			created[bareTable(m[1])] = true
			continue
		}
		if m := reAlterTable.FindStringSubmatch(t); m != nil && !created[bareTable(m[1])] {
			table := bareTable(m[1])
			if reAddConstraintFKCheck.MatchString(t) && !reNotValid.MatchString(t) {
				bad = append(bad, "ADD FOREIGN KEY/CHECK on "+table+" without NOT VALID (scans the table under a write-blocking lock; add it NOT VALID, VALIDATE CONSTRAINT in a later file)")
			}
			if reAddConstraintIndexed.MatchString(t) && !reUsingIndex.MatchString(t) {
				bad = append(bad, "ADD UNIQUE/PRIMARY KEY on "+table+" builds an index under a lock (CREATE UNIQUE INDEX CONCURRENTLY first, then ADD CONSTRAINT ... USING INDEX)")
			}
			for _, c := range reAddColumn.FindAllStringSubmatch(t, -1) {
				if reInlineIndexed.MatchString(c[1]) {
					bad = append(bad, "ADD COLUMN with UNIQUE/PRIMARY KEY on "+table+" builds an index under a lock")
				}
			}
		}
	}
	return bad
}

// v2CompatFindings is lint 3: changes that make the previous release's writes fail. A table created in the same file is exempt.
func v2CompatFindings(src string) []string {
	var bad []string
	created := map[string]bool{}
	for _, st := range sqlStatements(src) {
		t := st.text
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			created[bareTable(m[1])] = true
			continue
		}
		if m := reCreateUniqueIndex.FindStringSubmatch(t); m != nil && !created[bareTable(m[1])] {
			bad = append(bad, "CREATE UNIQUE INDEX on "+bareTable(m[1]))
			continue
		}
		m := reAlterTable.FindStringSubmatch(t)
		if m == nil || created[bareTable(m[1])] {
			continue
		}
		table := bareTable(m[1])
		if reAddConstraintFKCheck.MatchString(t) && strings.Contains(strings.ToLower(reAddConstraintFKCheck.FindString(t)), "check") {
			bad = append(bad, "a new CHECK on "+table)
		}
		if reAddConstraintIndexed.MatchString(t) {
			bad = append(bad, "a new UNIQUE/PRIMARY KEY on "+table)
		}
		if reSetNotNull.MatchString(t) {
			bad = append(bad, "SET NOT NULL on "+table)
		}
		for _, c := range reAddColumn.FindAllStringSubmatch(t, -1) {
			col := c[1]
			if reNotNull.MatchString(col) && !reHasDefault.MatchString(col) {
				bad = append(bad, "ADD COLUMN NOT NULL without a DEFAULT on "+table)
			}
			if reInlineCheck.MatchString(col) {
				bad = append(bad, "ADD COLUMN with a CHECK on "+table)
			}
			if reInlineIndexed.MatchString(col) {
				bad = append(bad, "ADD COLUMN with UNIQUE/PRIMARY KEY on "+table)
			}
		}
	}
	return bad
}

// v2TimeoutFindings is lint 4.
func v2TimeoutFindings(src string) []string {
	if reDaAckRewrite.MatchString(src) {
		return nil
	}
	var bad []string
	for _, st := range sqlStatements(src) {
		if m := reLockTimeoutSetAny.FindStringSubmatch(st.text); m != nil && timeoutValue(m[1], m[2]) > maxLockTimeoutV2 {
			bad = append(bad, "lock_timeout above 2s")
		}
		if m := reStatementTimeoutSet.FindStringSubmatch(st.text); m != nil {
			if d := timeoutValue(m[1], m[2]); d == 0 || d > 60*time.Second {
				bad = append(bad, "statement_timeout above 60s or unlimited")
			}
		}
	}
	return bad
}

func TestEveryNewMigrationPassesV2Lints(t *testing.T) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	checked := 0
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		if n, ok := migrationNumber(base); ok && n < lintV2From {
			continue
		}
		checked++
		body, _ := fs.ReadFile(migrationFS, name)
		src := string(body)
		if reNoTxDirective.MatchString(src) {
			continue
		}
		if bad := v2LockFindings(src); len(bad) > 0 {
			t.Errorf("%s: %s", base, strings.Join(bad, "; "))
		}
		if bad := v2CompatFindings(src); len(bad) > 0 && !contractMarker.MatchString(src) {
			t.Errorf("%s: %s would make the previous release's writes fail while it still runs; add a line \"-- contract: <why no running version needs this>\" and get the devils-advocate's ack (the two-release rule then applies)", base, strings.Join(bad, "; "))
		}
		if bad := v2TimeoutFindings(src); len(bad) > 0 {
			t.Errorf("%s: %s; add \"-- da-ack: rewrite <reason>\" once DA has acked it on the PR", base, strings.Join(bad, "; "))
		}
	}
	if checked == 0 {
		t.Fatal("no migration at or after lintV2From was checked")
	}
}

func TestV2LockFindings(t *testing.T) {
	for _, bad := range []string{
		"ALTER TABLE drives ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES b (id);",
		"ALTER TABLE drives ADD FOREIGN KEY (a) REFERENCES b (id);",
		"ALTER TABLE drives ADD CONSTRAINT c CHECK (a > 0);",
		"ALTER TABLE drives ADD CONSTRAINT u UNIQUE (a);",
		"ALTER TABLE drives ADD PRIMARY KEY (id);",
		"ALTER TABLE drives ADD COLUMN k text UNIQUE;",
	} {
		if len(v2LockFindings(bad)) == 0 {
			t.Errorf("lock lint misses %q", bad)
		}
	}
	for _, fine := range []string{
		"ALTER TABLE drives ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES b (id) NOT VALID;",
		"ALTER TABLE drives ADD CONSTRAINT c CHECK (a > 0) NOT VALID;",
		"ALTER TABLE drives VALIDATE CONSTRAINT c;",
		"ALTER TABLE drives ADD CONSTRAINT u UNIQUE USING INDEX drives_u_idx;",
		"CREATE TABLE fresh (id int);\nALTER TABLE fresh ADD CONSTRAINT u UNIQUE (id);",
		"ALTER TABLE drives ADD COLUMN k text;",
	} {
		if bad := v2LockFindings(fine); len(bad) > 0 {
			t.Errorf("lock lint wrongly flags %q: %v", fine, bad)
		}
	}
}

func TestV2CompatFindings(t *testing.T) {
	for _, bad := range []string{
		"ALTER TABLE drives ADD COLUMN k int NOT NULL;",
		"ALTER TABLE drives ADD CONSTRAINT c CHECK (a > 0) NOT VALID;",
		"ALTER TABLE drives ADD COLUMN k int CHECK (k > 0);",
		"ALTER TABLE drives ADD CONSTRAINT u UNIQUE USING INDEX i;",
		"CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS i ON drives (a);",
		"ALTER TABLE drives ALTER COLUMN a SET NOT NULL;",
	} {
		if len(v2CompatFindings(bad)) == 0 {
			t.Errorf("compat lint misses %q", bad)
		}
	}
	for _, fine := range []string{
		"ALTER TABLE drives ADD COLUMN k int NOT NULL DEFAULT 0;",
		"ALTER TABLE drives ADD COLUMN k int;",
		"ALTER TABLE drives ADD CONSTRAINT f FOREIGN KEY (a) REFERENCES b (id) NOT VALID;",
		"ALTER TABLE drives VALIDATE CONSTRAINT c;",
		"CREATE TABLE fresh (id int NOT NULL, k int CHECK (k > 0));\nCREATE UNIQUE INDEX fresh_k ON fresh (k);\nALTER TABLE fresh ALTER COLUMN k SET NOT NULL;",
		"CREATE INDEX i ON drives (a);",
	} {
		if bad := v2CompatFindings(fine); len(bad) > 0 {
			t.Errorf("compat lint wrongly flags %q: %v", fine, bad)
		}
	}
}

func TestV2TimeoutFindings(t *testing.T) {
	for _, bad := range []string{
		"SET LOCAL lock_timeout = '5s';",
		"SET LOCAL lock_timeout = '3000ms';",
		"SET LOCAL statement_timeout = 0;",
		"SET LOCAL statement_timeout = '2min';",
	} {
		if len(v2TimeoutFindings(bad)) == 0 {
			t.Errorf("timeout lint misses %q", bad)
		}
	}
	for _, fine := range []string{
		"SET LOCAL lock_timeout = '2s';",
		"SET LOCAL lock_timeout = '500ms';",
		"SET LOCAL statement_timeout = '30s';",
		"-- da-ack: rewrite the table is tiny and the statement is slow once\nSET LOCAL statement_timeout = '5min';",
	} {
		if bad := v2TimeoutFindings(fine); len(bad) > 0 {
			t.Errorf("timeout lint wrongly flags %q: %v", fine, bad)
		}
	}
}

// Calibration, not a rule: what the v2 lints would have said about 0109 to 0119 (the done-when of step 4 expects 0112 to 0118 to pass the probe;
// here, the static lints). Logged with -v; it never fails.
func TestV2LintsOverHistory(t *testing.T) {
	names, _ := fs.Glob(migrationFS, "migrations/*.sql")
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		if n, ok := migrationNumber(base); !ok || n >= lintV2From || n < lintAllFrom {
			continue
		}
		body, _ := fs.ReadFile(migrationFS, name)
		src := string(body)
		var all []string
		all = append(all, v2LockFindings(src)...)
		if !contractMarker.MatchString(src) {
			all = append(all, v2CompatFindings(src)...)
		}
		all = append(all, v2TimeoutFindings(src)...)
		if len(all) > 0 {
			t.Logf("%s would say: %s", base, strings.Join(all, "; "))
		}
	}
}
