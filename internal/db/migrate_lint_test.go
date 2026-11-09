package db

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
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
	reDropIndex   = regexp.MustCompile(`(?is)^drop\s+index\b`)
	reDoBlock     = regexp.MustCompile(`(?is)^do\b`)
	reDynamicDDL  = regexp.MustCompile(`(?is)\b(alter\s+table|create\s+(?:unique\s+)?index|create\s+(?:or\s+replace\s+)?(?:constraint\s+)?trigger|drop\s+trigger|drop\s+table|truncate)\b`)
)

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
		if boundedLockTO.MatchString(t) {
			bounded = true
			continue
		}
		if m := reCreateTable.FindStringSubmatch(t); m != nil {
			created[bareTable(m[1])] = true
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
		if bad := lockFindings(string(body)); len(bad) > 0 {
			t.Errorf("%s: %s without a bounded SET LOCAL lock_timeout = '5s' before it (a table created in the same file is exempt)", base, strings.Join(bad, ", "))
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
		"CREATE TABLE a (id int);\nALTER TABLE b ADD COLUMN x int;",        // another table
		"ALTER TABLE users ADD COLUMN x int;\n" + set,                      // the timeout comes after
		"SET LOCAL lock_timeout = 0;\nALTER TABLE users ADD COLUMN x int;", // zero is unbounded
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
		"SELECT 1;",
	} {
		if bad := lockFindings(fine); len(bad) > 0 {
			t.Errorf("lint wrongly flags %q: %v", fine, bad)
		}
	}
}

// Calibration, not a rule: how the lint would have judged every migration before lintAllFrom. Logged so a reviewer sees what a stricter start
// would have caught (run with -v). It never fails.
func TestLockFindingsOverHistory(t *testing.T) {
	names, _ := fs.Glob(migrationFS, "migrations/*.sql")
	sort.Strings(names)
	flagged := 0
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		if n, ok := migrationNumber(base); !ok || n >= lintAllFrom {
			continue
		}
		body, _ := fs.ReadFile(migrationFS, name)
		if bad := lockFindings(string(body)); len(bad) > 0 {
			flagged++
		}
	}
	t.Logf("%d of %d earlier migrations would be flagged by the all-tables rule (not enforced)", flagged, len(names))
}
