package db

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The migration lint, every table (zero-impact deploys, G6a part 1). A statement that locks a table other sessions use waits behind a long
// write and then queues every write behind it, so a migration that touches an EXISTING table must first SET LOCAL a bounded lock_timeout:
// a busy table then fails the migration, which retries on the next boot, instead of stalling the recorder.
//
// Checked: ALTER TABLE, CREATE INDEX, CREATE TRIGGER, DROP TRIGGER, DROP INDEX, DROP TABLE and TRUNCATE on any table not created earlier in the
// same file (a table nobody else can see yet needs nothing), and a DO block that runs any of them (its tables are named at run time, so it counts
// as touching an existing one). The older checks (migrate_locktimeout_test.go: vehicle_state from 0054, charges/drives/vehicles from 0108) stay;
// this one covers all tables from lintAllFrom on, so history is not re-litigated.
const lintAllFrom = 109

// backfillLintFrom: data-moving statements are refused from migration 0110 on (DA: no warning period). The list of small tables below is the one
// exemption, and changing it needs DA's ack.
const backfillLintFrom = 110

// sqlStatements splits a migration into its statements, comments removed, string literals and dollar-quoted bodies kept whole (a DO block
// or function body is one statement). A statement carries the byte offset where it starts in the original file.
type sqlStmt struct {
	text string
	at   int
}

func sqlStatements(src string) []sqlStmt {
	var out []sqlStmt
	var cur strings.Builder
	start := -1
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, sqlStmt{text: t, at: start})
		}
		cur.Reset()
		start = -1
	}
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '-' && strings.HasPrefix(src[i:], "--"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				i = len(src)
			} else {
				i += 2 + end + 2
			}
		case c == '\'':
			j := i + 1
			for j < len(src) {
				if src[j] == '\'' {
					if j+1 < len(src) && src[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if start < 0 {
				start = i
			}
			cur.WriteString(src[i:min(j+1, len(src))])
			i = j + 1
		case c == '$':
			m := dollarTag.FindString(src[i:])
			if m == "" {
				if start < 0 {
					start = i
				}
				cur.WriteByte(c)
				i++
				continue
			}
			end := strings.Index(src[i+len(m):], m)
			stop := len(src)
			if end >= 0 {
				stop = i + len(m) + end + len(m)
			}
			if start < 0 {
				start = i
			}
			cur.WriteString(src[i:stop])
			i = stop
		case c == ';':
			flush()
			i++
		default:
			if start < 0 && c != ' ' && c != '\n' && c != '\t' && c != '\r' {
				start = i
			}
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

var dollarTag = regexp.MustCompile(`^\$[A-Za-z_]*\$`)

var (
	reCreateTable = regexp.MustCompile(`(?is)^create\s+(?:(?:global\s+|local\s+)?(?:temp|temporary|unlogged)\s+)?table\s+(?:if\s+not\s+exists\s+)?(\S+)`)
	reAlterTable  = regexp.MustCompile(`(?is)^alter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?(\S+)`)
	reCreateIndex = regexp.MustCompile(`(?is)^create\s+(?:unique\s+)?index\s+(?:concurrently\s+)?(?:if\s+not\s+exists\s+)?\S*\s*on\s+(?:only\s+)?(\S+)`)
	reCreateTrig  = regexp.MustCompile(`(?is)^create\s+(?:or\s+replace\s+)?(?:constraint\s+)?trigger\b.*?\bon\s+(?:only\s+)?(\S+)`)
	reDropTrig    = regexp.MustCompile(`(?is)^drop\s+trigger\s+(?:if\s+exists\s+)?\S+\s+on\s+(\S+)`)
	reDropTable   = regexp.MustCompile(`(?is)^drop\s+table\s+(?:if\s+exists\s+)?(\S+)`)
	reTruncate    = regexp.MustCompile(`(?is)^truncate\s+(?:table\s+)?(?:only\s+)?(\S+)`)
	// A foreign key (inline REFERENCES or table-level FOREIGN KEY ... REFERENCES) takes SHARE ROW EXCLUSIVE on the referenced table.
	reReferences = regexp.MustCompile(`(?is)\breferences\s+("?[A-Za-z_][A-Za-z0-9_.]*"?)`)
	reDropIndex  = regexp.MustCompile(`(?is)^drop\s+index\b`)
	reDoBlock    = regexp.MustCompile(`(?is)^do\b`)
	reDynamicDDL = regexp.MustCompile(`(?is)\b(alter\s+table|create\s+(?:unique\s+)?index|create\s+(?:or\s+replace\s+)?(?:constraint\s+)?trigger|drop\s+trigger|drop\s+table|truncate)\b`)
)

// lockTimeoutSet matches a SET LOCAL lock_timeout and captures its value and unit. Postgres reads a bare number as milliseconds.
var lockTimeoutSet = regexp.MustCompile(`(?is)^set\s+local\s+lock_timeout\s*(?:=|to)\s*'?\s*(\d+)\s*(ms|s|min|h|d)?\s*'?\s*$`)

// maxLockTimeout is the longest wait a migration may allow: it bounds how long a migration can queue writes behind itself. 0 means no limit.
const maxLockTimeout = 10 * time.Second

// boundedLockTimeout is true for a SET LOCAL lock_timeout of 1 ms to 10 s.
func boundedLockTimeout(stmt string) bool {
	m := lockTimeoutSet.FindStringSubmatch(stmt)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return false
	}
	unit := map[string]time.Duration{"": time.Millisecond, "ms": time.Millisecond, "s": time.Second, "min": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[strings.ToLower(m[2])]
	return time.Duration(n)*unit <= maxLockTimeout
}

func bareTable(s string) string {
	s = strings.TrimRight(s, "(,;")
	s = strings.TrimPrefix(strings.ToLower(s), "public.")
	return strings.Trim(s, `"`)
}

// lockFindings lists the statements in a migration that lock an existing table without a bounded lock_timeout before them.
func lockFindings(src string) []string {
	var bad []string
	created := map[string]bool{}
	bounded := false
	for _, st := range sqlStatements(src) {
		t := st.text
		if boundedLockTimeout(t) {
			bounded = true
			continue
		}
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			self := bareTable(m[1])
			for _, r := range reReferences.FindAllStringSubmatch(t, -1) {
				if ref := bareTable(r[1]); ref != self && !created[ref] && !bounded {
					bad = append(bad, "a foreign key to "+ref)
				}
			}
			created[self] = true
			continue
		}
		var table, what string
		for _, re := range []struct {
			re   *regexp.Regexp
			what string
		}{{reAlterTable, "ALTER TABLE"}, {reCreateIndex, "CREATE INDEX"}, {reCreateTrig, "CREATE TRIGGER"}, {reDropTrig, "DROP TRIGGER"}, {reDropTable, "DROP TABLE"}, {reTruncate, "TRUNCATE"}} {
			if m := re.re.FindStringSubmatch(t); m != nil {
				table, what = bareTable(m[1]), re.what
				break
			}
		}
		if what == "ALTER TABLE" {
			// ADD [CONSTRAINT x] FOREIGN KEY ... REFERENCES other / ADD COLUMN ... REFERENCES other: a lock on the other table as well.
			for _, r := range reReferences.FindAllStringSubmatch(t, -1) {
				if ref := bareTable(r[1]); ref != table && !created[ref] && !bounded {
					bad = append(bad, "a foreign key to "+ref)
				}
			}
		}
		switch {
		case what != "":
			if !created[table] && !bounded {
				bad = append(bad, what+" "+table)
			}
		case reDropIndex.MatchString(t):
			if !bounded {
				bad = append(bad, "DROP INDEX")
			}
		case reDoBlock.MatchString(t) && reDynamicDDL.MatchString(t):
			if !bounded {
				bad = append(bad, "a DO block that alters tables")
			}
		}
	}
	return bad
}

// 1b: lock_timeout bounds the wait for a lock, not how long it is held. DDL that rewrites the table (or scans it all under the lock) holds ACCESS
// EXCLUSIVE for as long as the table is big, so on an existing table it needs DA's per-PR ack: a "-- da-ack: rewrite <reason>" line in the file.
var (
	reAlterType    = regexp.MustCompile(`(?is)\balter\s+column\s+\S+\s+(?:set\s+data\s+)?type\b`)
	reSetNotNull   = regexp.MustCompile(`(?is)\balter\s+column\s+\S+\s+set\s+not\s+null\b`)
	reAddColumn    = regexp.MustCompile(`(?is)\badd\s+column\b(.*?)(?:,\s*(?:add|drop|alter)\b|$)`)
	reVolatile     = regexp.MustCompile(`(?is)\bdefault\b[^,]*?\b(random|gen_random_uuid|gen_random_bytes|uuid_generate_v\d|clock_timestamp|timeofday|nextval|txid_current)\s*\(`)
	reRewriteCol   = regexp.MustCompile(`(?is)\b(?:small|big)?serial\b|\bgenerated\s+always\s+as\s*\(.*\)\s*stored\b|\bgenerated\s+(?:always|by\s+default)\s+as\s+identity\b`)
	reVacuumFull   = regexp.MustCompile(`(?is)^vacuum\s*(?:\([^)]*\bfull\b[^)]*\)|full\b)`)
	reCluster      = regexp.MustCompile(`(?is)^cluster\b`)
	reReindex      = regexp.MustCompile(`(?is)^reindex\b`)
	reConcurrently = regexp.MustCompile(`(?is)\bconcurrently\b`)
	reValidate     = regexp.MustCompile(`(?is)\bvalidate\s+constraint\b`)
	reDaAckRewrite = regexp.MustCompile(`(?im)^\s*--\s*da-ack:\s*rewrite\s+\S+`)
)

// rewriteFindings lists the statements that can rewrite or fully scan an existing table under a lock, unless the file carries the ack.
func rewriteFindings(src string) []string {
	if reDaAckRewrite.MatchString(src) {
		return nil
	}
	var bad []string
	created := map[string]bool{}
	validated := false
	for _, st := range sqlStatements(src) {
		t := st.text
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			created[bareTable(m[1])] = true
			continue
		}
		if reValidate.MatchString(t) {
			validated = true
		}
		switch {
		case reVacuumFull.MatchString(t):
			bad = append(bad, "VACUUM FULL")
		case reCluster.MatchString(t):
			bad = append(bad, "CLUSTER")
		case reReindex.MatchString(t) && !reConcurrently.MatchString(t):
			bad = append(bad, "REINDEX without CONCURRENTLY")
		}
		m := reAlterTable.FindStringSubmatch(t)
		if m == nil || created[bareTable(m[1])] {
			continue
		}
		table := bareTable(m[1])
		if reAlterType.MatchString(t) {
			bad = append(bad, "ALTER COLUMN TYPE on "+table)
		}
		if reSetNotNull.MatchString(t) && !validated {
			bad = append(bad, "SET NOT NULL on "+table+" without a validated CHECK before it")
		}
		for _, c := range reAddColumn.FindAllStringSubmatch(t, -1) {
			if reVolatile.MatchString("default "+c[1]) || reVolatile.MatchString(c[1]) || reRewriteCol.MatchString(c[1]) {
				bad = append(bad, "ADD COLUMN with a volatile default, serial, identity or stored generated column on "+table)
			}
		}
	}
	return bad
}

// Part 3: data moves out of migrations (zero-impact deploys, G6b). A migration changes schema; moving data after a schema change is a background
// backfill (internal/backfill: keyset batches in short transactions, one pod at a time, resumable). A table-wide UPDATE, a DELETE or an
// INSERT ... SELECT on an existing table inside a migration holds row locks in one long transaction, bloats the table and holds up the migration job
// that runs before every rollout. So a statement of that kind on a table that is neither created in the same file nor on smallTables (tables bounded
// by design) needs "-- da-ack: backfill-in-migration <reason>" (DA acks per PR, with the row estimate), unless it is a job. An INSERT ... SELECT is
// judged by what it READS, whatever the target (copying a growable table into a new one is the same long scan): any FROM or JOIN table that
// existed before the file and can grow counts. A DO block is not parsed.
var (
	reUpdateStmt    = regexp.MustCompile(`(?is)^update\s+(?:only\s+)?(\S+)`)
	reDeleteStmt    = regexp.MustCompile(`(?is)^delete\s+from\s+(?:only\s+)?(\S+)`)
	reInsertSelect  = regexp.MustCompile(`(?is)^insert\s+into\s+(\S+)[^;]*?\bselect\b(.*)$`)
	reFromJoinTable = regexp.MustCompile(`(?is)\b(?:from|join)\s+(?:only\s+)?([A-Za-z_][A-Za-z0-9_.]*)`)
	reDaAckBackfill = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*da-ack:[ \t]*backfill-in-migration[ \t]+\S+`) // same line: a bare ack must not borrow the next line as its reason
)

// backfillFindings lists the data-moving statements of a migration on existing growing tables, unless the file carries the ack.
func backfillFindings(src string) []string {
	if reDaAckBackfill.MatchString(src) {
		return nil
	}
	var bad []string
	created := map[string]bool{}
	for _, st := range sqlStatements(src) {
		t := st.text
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			created[bareTable(m[1])] = true
			continue
		}
		var targets []string
		what := ""
		switch {
		case reUpdateStmt.MatchString(t):
			targets, what = []string{bareTable(reUpdateStmt.FindStringSubmatch(t)[1])}, "UPDATE"
		case reDeleteStmt.MatchString(t):
			targets, what = []string{bareTable(reDeleteStmt.FindStringSubmatch(t)[1])}, "DELETE"
		case reInsertSelect.MatchString(t):
			// By what it reads: the FROM and JOIN tables of the select part.
			for _, m := range reFromJoinTable.FindAllStringSubmatch(reInsertSelect.FindStringSubmatch(t)[2], -1) {
				targets = append(targets, bareTable(m[1]))
			}
			what = "INSERT ... SELECT from"
		default:
			continue
		}
		for _, table := range targets {
			if created[table] {
				continue
			}
			if _, small := smallTables[table]; small {
				continue
			}
			bad = append(bad, what+" "+table)
		}
	}
	return bad
}

// Part 2: indexes on existing tables. A plain CREATE INDEX takes a SHARE lock that blocks every write for as long as the build takes, so on a table that
// can grow it must be CREATE INDEX CONCURRENTLY IF NOT EXISTS, alone in a file whose first line is "-- migrate:no-transaction" (the runner,
// migrate_notx.go, sets the timeouts and repairs an invalid leftover). How the lint knows a table can grow: it assumes every existing table can, except the
// short list below of tables bounded by DESIGN (a lookup, a singleton or an install-wide table). Anything with a row per user, vehicle, session or event is
// never on it, however small it is today, so a table forgotten here fails safe: its index costs a file of its own. No row counts in this list.
// vehicle_state (partitioned: CONCURRENTLY is not allowed on a partitioned parent) takes no new index at all until a design for it exists.
var smallTables = map[string]string{
	"migrations":   "the migration bookkeeping table: one row per migration",
	"flags":        "one global row of operator switches",
	"app_settings": "install-wide key/value settings: a fixed set of keys",
	"push_vapid":   "one VAPID keypair per install",
	"lease_pods":   "one row per live pod, pruned after a day",
}

var (
	reNoTxDirective = regexp.MustCompile(`(?m)\A\s*--\s*migrate:no-transaction\s*$`)
	reCIC           = regexp.MustCompile(`(?is)^create\s+(?:unique\s+)?index\s+concurrently\b`)
	reCICStrict     = regexp.MustCompile(`(?is)^create\s+(?:unique\s+)?index\s+concurrently\s+if\s+not\s+exists\s+\S+\s+on\s+(?:only\s+)?(\S+)`)
	reVehicleState  = regexp.MustCompile(`^vehicle_state(_.*)?$`)
)

// indexFindings judges the index statements of a migration (see the comment above).
func indexFindings(src string) []string {
	noTx := reNoTxDirective.MatchString(src)
	stmts := sqlStatements(src)
	var bad []string
	created := map[string]bool{}
	if noTx {
		if len(stmts) != 1 {
			return []string{"a no-transaction file must be exactly one CREATE INDEX CONCURRENTLY IF NOT EXISTS statement"}
		}
		m := reCICStrict.FindStringSubmatch(stmts[0].text)
		if m == nil {
			return []string{"a no-transaction file must be exactly one CREATE INDEX CONCURRENTLY IF NOT EXISTS statement"}
		}
		if reVehicleState.MatchString(bareTable(m[1])) {
			return []string{"an index on the partitioned vehicle_state (not supported: no design for it yet; ask DA)"}
		}
		return nil
	}
	for _, st := range stmts {
		t := st.text
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			created[bareTable(m[1])] = true
			continue
		}
		if reCIC.MatchString(t) {
			bad = append(bad, "CREATE INDEX CONCURRENTLY outside a no-transaction file (it fails inside a transaction): put it alone in a file whose first line is -- migrate:no-transaction")
			continue
		}
		m := reCreateIndex.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		table := bareTable(m[1])
		switch {
		case created[table]:
		case reVehicleState.MatchString(table):
			bad = append(bad, "an index on the partitioned vehicle_state (not supported: no design for it yet; ask DA)")
		default:
			if _, small := smallTables[table]; !small {
				bad = append(bad, "CREATE INDEX on "+table+" (an existing table that can grow): use CREATE INDEX CONCURRENTLY IF NOT EXISTS alone in a file starting with -- migrate:no-transaction")
			}
		}
	}
	return bad
}

func migrationNumber(base string) (int, bool) {
	n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
	return n, err == nil
}

func TestEveryExistingTableLockSetsLockTimeout(t *testing.T) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		// A date-prefixed name (allowed by Migrate) is newer than any numbered one, so it is checked too.
		if n, ok := migrationNumber(base); ok && n < lintAllFrom {
			continue
		}
		body, err := fs.ReadFile(migrationFS, name)
		if err != nil {
			t.Fatal(err)
		}
		if bad := indexFindings(string(body)); len(bad) > 0 {
			t.Errorf("%s: %s", base, strings.Join(bad, "; "))
		}
		// A no-transaction file is one CREATE INDEX CONCURRENTLY: the runner sets lock_timeout and statement_timeout itself.
		if reNoTxDirective.MatchString(string(body)) {
			continue
		}
		if bad := lockFindings(string(body)); len(bad) > 0 {
			t.Errorf("%s: %s without a bounded (1 ms to 10 s) SET LOCAL lock_timeout = '5s' before it (a table created in the same file is exempt)", base, strings.Join(bad, ", "))
		}
		if n, ok := migrationNumber(base); !ok || n >= backfillLintFrom { // a date-prefixed name is newer than any numbered one
			if bad := backfillFindings(string(body)); len(bad) > 0 {
				t.Errorf("%s: %s moves data in a migration; schema only here, move the data with a backfill job (internal/backfill), or add a line \"-- da-ack: backfill-in-migration <reason>\" once DA has acked it on the PR", base, strings.Join(bad, ", "))
			}
		}
		if bad := rewriteFindings(string(body)); len(bad) > 0 {
			t.Errorf("%s: %s can rewrite or scan an existing table under a lock; add a line \"-- da-ack: rewrite <reason>\" once DA has acked it on the PR", base, strings.Join(bad, ", "))
		}
	}
}

func TestLockFindings(t *testing.T) {
	set := "SET LOCAL lock_timeout = '5s';\n"
	for _, bad := range []string{
		"ALTER TABLE users ADD COLUMN x int;",
		"alter table only public.sessions add column x int",
		`ALTER TABLE "settings" DROP COLUMN y;`,
		"CREATE INDEX i ON users (email);",
		"CREATE UNIQUE INDEX IF NOT EXISTS i ON public.users (email);",
		"CREATE TRIGGER t BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION f();",
		"DROP TRIGGER IF EXISTS t ON users;",
		"DROP TABLE old_things;",
		"TRUNCATE TABLE caches;",
		"DROP INDEX some_idx;",
		"DO $$ BEGIN EXECUTE 'ALTER TABLE users ADD COLUMN z int'; END $$;",
		"CREATE TABLE a (id int);\nALTER TABLE b ADD COLUMN x int;",                                                        // another table
		"ALTER TABLE users ADD COLUMN x int;\n" + set,                                                                      // the timeout comes after
		"SET LOCAL lock_timeout = 0;\nALTER TABLE users ADD COLUMN x int;",                                                 // zero is unbounded
		"CREATE TABLE notes (id int, user_id uuid REFERENCES users (id));",                                                 // inline REFERENCES locks users
		"CREATE TABLE notes (id int, user_id uuid, FOREIGN KEY (user_id) REFERENCES public.users (id));",                   // table-level
		"CREATE TABLE a (id int);\nCREATE TABLE notes (id int, a_id int REFERENCES a (id), u uuid REFERENCES users (id));", // one is new, one is not
		"CREATE TABLE a (id int);\nALTER TABLE a ADD COLUMN u uuid REFERENCES users (id);",                                 // ADD COLUMN ... REFERENCES
		"CREATE TABLE a (id int, u uuid);\nALTER TABLE a ADD CONSTRAINT fk FOREIGN KEY (u) REFERENCES users (id);",         // ADD CONSTRAINT
	} {
		if len(lockFindings(bad)) == 0 {
			t.Errorf("lint misses %q", bad)
		}
	}
	for _, fine := range []string{
		set + "ALTER TABLE users ADD COLUMN x int;",
		set + "CREATE INDEX i ON users (email);\nCREATE TRIGGER t BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION f();",
		"CREATE TABLE fresh (id int);\nALTER TABLE fresh ADD COLUMN x int;\nCREATE INDEX i ON fresh (x);",
		"CREATE TABLE IF NOT EXISTS public.fresh (id int);\nCREATE TRIGGER t BEFORE INSERT ON fresh FOR EACH ROW EXECUTE FUNCTION f();",
		"-- ALTER TABLE users ADD COLUMN in a comment\nSELECT 1;",
		"/* DROP TABLE users; */ SELECT 1;",
		"INSERT INTO t (note) VALUES ('ALTER TABLE users ADD COLUMN x int');",
		"CREATE FUNCTION f() RETURNS trigger AS $$ BEGIN ALTER TABLE users ADD COLUMN x int; RETURN NEW; END $$ LANGUAGE plpgsql;", // defined, not run
		"CREATE TABLE a (id int);\nCREATE TABLE b (LIKE a);\nCREATE INDEX i ON b (id);",
		"CREATE TABLE a (id int PRIMARY KEY);\nCREATE TABLE b (id int, a_id int REFERENCES a (id));", // references a table made here
		"CREATE TABLE tree (id int PRIMARY KEY, parent int REFERENCES tree (id));",                   // self reference
		set + "CREATE TABLE notes (id int, user_id uuid REFERENCES users (id));",                     // bounded first
		"-- REFERENCES users in a comment\nCREATE TABLE plain (id int);",
		"SELECT 1;",
	} {
		if bad := lockFindings(fine); len(bad) > 0 {
			t.Errorf("lint wrongly flags %q: %v", fine, bad)
		}
	}
}

func TestIndexFindings(t *testing.T) {
	noTx := "-- migrate:no-transaction\n"
	for _, bad := range []string{
		"CREATE INDEX i ON drives (user_id);",
		"CREATE UNIQUE INDEX IF NOT EXISTS i ON public.users (email);",
		"SET LOCAL lock_timeout = '5s';\nCREATE INDEX i ON sessions (user_id);", // bounded is not enough for a table that can grow
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON drives (user_id);",        // not in a no-transaction file
		"CREATE INDEX i ON vehicle_state (at);",
		"CREATE INDEX i ON vehicle_state_2026_11 (at);",
		noTx + "CREATE INDEX CONCURRENTLY i ON drives (user_id);", // no IF NOT EXISTS
		noTx + "CREATE INDEX CONCURRENTLY IF NOT EXISTS a ON drives (x);\nCREATE INDEX CONCURRENTLY IF NOT EXISTS b ON drives (y);",
		noTx + "ALTER TABLE drives ADD COLUMN x int;",
		noTx + "CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON vehicle_state (at);",
	} {
		if len(indexFindings(bad)) == 0 {
			t.Errorf("index lint misses %q", bad)
		}
	}
	for _, fine := range []string{
		noTx + "-- why: the table grows\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS i ON drives (user_id, started_at);",
		"CREATE TABLE fresh (id int, k int);\nCREATE INDEX i ON fresh (k);",
		"SET LOCAL lock_timeout = '5s';\nCREATE INDEX i ON app_settings (updated_at);",
		"CREATE INDEX i ON lease_pods (seen_at);",
		"CREATE TABLE a (id int);\nSELECT 1;",
		"-- CREATE INDEX i ON drives (x) in a comment\nSELECT 1;",
	} {
		if bad := indexFindings(fine); len(bad) > 0 {
			t.Errorf("index lint wrongly flags %q: %v", fine, bad)
		}
	}
	for table, why := range smallTables {
		if strings.TrimSpace(why) == "" {
			t.Errorf("smallTables[%q] has no reason", table)
		}
	}
}

func TestBoundedLockTimeout(t *testing.T) {
	for _, ok := range []string{"SET LOCAL lock_timeout = '5s'", "set local lock_timeout to '10s'", "SET LOCAL lock_timeout = 3000", "SET LOCAL lock_timeout = '250ms'"} {
		if !boundedLockTimeout(ok) {
			t.Errorf("%q should be bounded", ok)
		}
	}
	for _, bad := range []string{"SET LOCAL lock_timeout = 0", "SET LOCAL lock_timeout = '0'", "SET LOCAL lock_timeout = '11s'", "SET LOCAL lock_timeout = '1min'", "SET lock_timeout = '5s'", "SET LOCAL lock_timeout = DEFAULT", "SET LOCAL statement_timeout = '5s'"} {
		if boundedLockTimeout(bad) {
			t.Errorf("%q should not count as bounded", bad)
		}
	}
}

func TestRewriteFindings(t *testing.T) {
	for _, bad := range []string{
		"ALTER TABLE users ALTER COLUMN age TYPE bigint;",
		"ALTER TABLE users ALTER COLUMN age SET DATA TYPE text;",
		"ALTER TABLE users ADD COLUMN token uuid DEFAULT gen_random_uuid();",
		"ALTER TABLE users ADD COLUMN seq bigserial;",
		"ALTER TABLE users ADD COLUMN full_name text GENERATED ALWAYS AS (a || b) STORED;",
		"ALTER TABLE users ALTER COLUMN age SET NOT NULL;",
		"ALTER TABLE users ADD CONSTRAINT c CHECK (age IS NOT NULL) NOT VALID;\nALTER TABLE users ALTER COLUMN age SET NOT NULL;", // not validated
		"VACUUM FULL users;",
		"VACUUM (FULL, ANALYZE) users;",
		"CLUSTER users USING users_pkey;",
		"REINDEX TABLE users;",
	} {
		if len(rewriteFindings(bad)) == 0 {
			t.Errorf("rewrite lint misses %q", bad)
		}
	}
	for _, fine := range []string{
		"ALTER TABLE users ADD COLUMN x int;",
		"ALTER TABLE users ADD COLUMN x int NOT NULL DEFAULT 0;",
		"ALTER TABLE users ADD COLUMN at timestamptz DEFAULT now();",
		"ALTER TABLE users ADD COLUMN tags text[] DEFAULT '{}';",
		"ALTER TABLE users ADD CONSTRAINT c CHECK (age IS NOT NULL) NOT VALID;\nALTER TABLE users VALIDATE CONSTRAINT c;\nALTER TABLE users ALTER COLUMN age SET NOT NULL;",
		"CREATE TABLE fresh (id int);\nALTER TABLE fresh ALTER COLUMN id TYPE bigint;",
		"VACUUM users;",
		"REINDEX INDEX CONCURRENTLY i;",
		"-- da-ack: rewrite the table is empty on every install (feature flag never shipped)\nALTER TABLE users ALTER COLUMN age TYPE bigint;",
		"ALTER TABLE users ADD COLUMN x int; -- not a serial, just a comment",
	} {
		if bad := rewriteFindings(fine); len(bad) > 0 {
			t.Errorf("rewrite lint wrongly flags %q: %v", fine, bad)
		}
	}
}

// Calibration, not a rule: how the lint would have judged every migration before lintAllFrom. Logged so a reviewer sees what a stricter start
// would have caught (run with -v). It never fails.
func TestLockFindingsOverHistory(t *testing.T) {
	names, _ := fs.Glob(migrationFS, "migrations/*.sql")
	sort.Strings(names)
	flagged, rewrites := 0, 0
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		if n, ok := migrationNumber(base); !ok || n >= lintAllFrom {
			continue
		}
		body, _ := fs.ReadFile(migrationFS, name)
		if bad := lockFindings(string(body)); len(bad) > 0 {
			flagged++
		}
		if bad := rewriteFindings(string(body)); len(bad) > 0 {
			rewrites++
			t.Logf("%s: %s", base, strings.Join(bad, ", "))
		}
	}
	t.Logf("%d of %d earlier migrations would be flagged by the all-tables rule, %d by the rewrite rule (not enforced)", flagged, len(names), rewrites)
}

func TestBackfillFindings(t *testing.T) {
	for name, tc := range map[string]struct {
		sql  string
		want int
	}{
		"schema only":                                        {"CREATE TABLE a (id int);\nALTER TABLE b ADD COLUMN x int;", 0},
		"update on an existing table":                        {"UPDATE drives SET x = 1;", 1},
		"update with a where is still data":                  {"UPDATE charges SET y = 2 WHERE y IS NULL;", 1},
		"delete on an existing table":                        {"DELETE FROM charges WHERE ended_at < now();", 1},
		"insert select reads a growable table":               {"INSERT INTO a2 (id) SELECT id FROM drives;", 1},
		"insert select reads a table created in the file":    {"CREATE TABLE n (id int);\nCREATE TABLE m (id int);\nINSERT INTO m (id) SELECT id FROM n;", 0},
		"insert select into a new table from a growable one": {"CREATE TABLE n (id int);\nINSERT INTO n (id) SELECT id FROM drives;", 1},
		"insert select from a small table":                   {"INSERT INTO a2 (k) SELECT k FROM flags;", 0},
		"insert select joins two":                            {"INSERT INTO a2 (id) SELECT d.id FROM drives d JOIN charges c ON c.id = d.id;", 2},
		"insert values is a seed":                            {"INSERT INTO flags (k) VALUES ('x') ON CONFLICT DO NOTHING;", 0},
		"table created in the same file":                     {"CREATE TABLE n (id int);\nUPDATE n SET id = 1;", 0},
		"a small table":                                      {"UPDATE flags SET v = 1;", 0},
		"acked":                                              {"-- da-ack: backfill-in-migration 40 rows, a lookup\nUPDATE drives SET x = 1;", 0},
		"the ack needs a reason":                             {"-- da-ack: backfill-in-migration\nUPDATE drives SET x = 1;", 1},
		"a do block is not parsed":                           {"DO $$ BEGIN UPDATE drives SET x = 1; END $$;", 0},
		"two findings":                                       {"UPDATE drives SET x = 1;\nDELETE FROM vehicles WHERE y;", 2},
		"schema-qualified":                                   {"UPDATE public.drives SET x = 1;", 1},
	} {
		if got := backfillFindings(tc.sql); len(got) != tc.want {
			t.Errorf("%s: %v, want %d finding(s)", name, got, tc.want)
		}
	}
}
