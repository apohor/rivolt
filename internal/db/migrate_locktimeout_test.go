package db

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// vehicle_state is the hottest table: an ALTER on it takes an ACCESS
// EXCLUSIVE lock, and waiting behind a long write queues every recorder
// insert behind it. Migrations after 0053 that ALTER it must first set a
// bounded lock_timeout (SET LOCAL; each migration runs in its own
// transaction), so a busy table fails the migration, which retries on
// the next boot, instead of stalling recording (DA note on #226).
const lockTimeoutFrom = 54

var (
	alterVehicleState = regexp.MustCompile(`(?is)\balter\s+table\s+(if\s+exists\s+)?(only\s+)?vehicle_state\b`)
	boundedLockTO     = regexp.MustCompile(`(?is)\bset\s+local\s+lock_timeout\s*(=|to)\s*'?\s*[1-9]`)
)

// The write-hot tables the recorder and the owner write: a trigger, an index or an ALTER on them takes a lock that waits behind a long write and then
// queues every write behind it (CREATE TRIGGER: SHARE ROW EXCLUSIVE; CREATE INDEX: SHARE; ALTER: ACCESS EXCLUSIVE). From migration 0108 each such statement
// needs a bounded lock_timeout before it (DA on 0108). Earlier migrations are not checked.
const hotTablesFrom = 108

var (
	hotTables = `(charges|drives|vehicles)`
	lockHot   = []*regexp.Regexp{
		regexp.MustCompile(`(?is)\balter\s+table\s+(if\s+exists\s+)?(only\s+)?` + hotTables + `\b`),
		regexp.MustCompile(`(?is)\bcreate\s+(or\s+replace\s+)?(constraint\s+)?trigger\b[^;]*?\bon\s+(only\s+)?` + hotTables + `\b`),
		regexp.MustCompile(`(?is)\bcreate\s+(unique\s+)?index\s+(if\s+not\s+exists\s+)?\S+\s+on\s+(only\s+)?` + hotTables + `\b`),
		// A trigger created in a DO block over a list of tables names them in an ARRAY and formats the table into the statement.
		regexp.MustCompile(`(?is)\bforeach\s+\w+\s+in\s+array\s+array\[[^\]]*'` + hotTables + `'[^\]]*\][^;]*?\bloop\b.*?\b(create\s+trigger|alter\s+table|create\s+(unique\s+)?index)\b`),
	}
)

func TestHotTableLocksSetLockTimeout(t *testing.T) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		if n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0]); err == nil && n < hotTablesFrom {
			continue
		}
		body, err := fs.ReadFile(migrationFS, name)
		if err != nil {
			t.Fatal(err)
		}
		set := boundedLockTO.FindIndex(body)
		for _, re := range lockHot {
			if at := re.FindIndex(body); at != nil && (set == nil || set[0] > at[0]) {
				t.Errorf("%s takes a lock on charges, drives or vehicles without first SET LOCAL lock_timeout = '5s' (or another bounded value)", base)
				break
			}
		}
	}
}

func TestHotTableGuardMatches(t *testing.T) {
	for _, bad := range []string{
		"ALTER TABLE charges ADD COLUMN x int;",
		"alter table only vehicles add column x int",
		"CREATE TRIGGER t BEFORE INSERT ON drives FOR EACH ROW EXECUTE FUNCTION f();",
		"CREATE INDEX IF NOT EXISTS i ON charges (user_id);",
		"CREATE UNIQUE INDEX i ON vehicles (vin);",
		"DO $$ DECLARE t text; BEGIN FOREACH t IN ARRAY ARRAY['charges', 'drives'] LOOP EXECUTE format('CREATE TRIGGER x ON %I', t); END LOOP; END $$;",
	} {
		hit := false
		for _, re := range lockHot {
			if re.FindIndex([]byte(bad)) != nil {
				hit = true
			}
		}
		if !hit {
			t.Errorf("guard misses %q", bad)
		}
	}
	for _, fine := range []string{
		"CREATE TABLE charges_capped_backup (id int);",
		"CREATE INDEX i ON charges_capped_backup (charge_id);",
		"ALTER TABLE vehicle_state ADD COLUMN x int;",
		"CREATE TRIGGER t BEFORE INSERT ON charges_deleted_backup FOR EACH ROW EXECUTE FUNCTION f();",
		"-- a comment about vehicles and drives",
	} {
		for _, re := range lockHot {
			if re.FindIndex([]byte(fine)) != nil {
				t.Errorf("guard wrongly matches %q", fine)
			}
		}
	}
}

func TestVehicleStateAltersSetLockTimeout(t *testing.T) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		// A date-prefixed name (allowed by Migrate) is newer than any
		// numbered one, so it is checked too.
		if n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0]); err == nil && n < lockTimeoutFrom {
			continue
		}
		body, err := fs.ReadFile(migrationFS, name)
		if err != nil {
			t.Fatal(err)
		}
		alter := alterVehicleState.FindIndex(body)
		if alter == nil {
			continue
		}
		set := boundedLockTO.FindIndex(body)
		if set == nil || set[0] > alter[0] {
			t.Errorf("%s alters vehicle_state without first SET LOCAL lock_timeout = '5s' (or another bounded value)", base)
		}
	}
}

func TestLockTimeoutGuardMatches(t *testing.T) {
	ok := "SET LOCAL lock_timeout = '5s';\nALTER TABLE vehicle_state ADD COLUMN x int;"
	if alterVehicleState.FindIndex([]byte(ok)) == nil || boundedLockTO.FindIndex([]byte(ok)) == nil {
		t.Fatal("guard misses a compliant migration")
	}
	for _, bad := range []string{
		"ALTER TABLE vehicle_state ADD COLUMN x int;",
		"SET LOCAL lock_timeout = 0;\nALTER TABLE IF EXISTS vehicle_state ADD COLUMN x int;",
	} {
		if boundedLockTO.FindIndex([]byte(bad)) != nil {
			t.Errorf("guard accepts %q", bad)
		}
	}
	if alterVehicleState.FindIndex([]byte("alter table vehicle_state_new add column x int")) != nil {
		t.Error("guard matches another table")
	}
}
