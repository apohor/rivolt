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
