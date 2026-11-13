//go:build integration

package db_test

import (
	"context"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The tables the repair role may write are exactly the tables the operator guard trigger is on: a table granted for writing without the
// trigger would let the repair role write it with no audit row, and a trigger on a table the role cannot write is dead code.
func TestRepairGuardTablesMatchTheGrants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, err := d.QueryContext(ctx, `SELECT c.relname FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid
		WHERE tg.tgname = 'rivolt_operator_guard' AND NOT tg.tgisinternal ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	var guarded []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		guarded = append(guarded, n)
	}
	rows.Close()
	want := db.RepairWriteTables()
	sort.Strings(want)
	if !reflect.DeepEqual(guarded, want) {
		t.Fatalf("guard trigger on %v, repair role may write %v", guarded, want)
	}
}

// Everything that holds a token, a key, a credential or an identifier a repair never needs is closed to the repair role. A table or column that
// matches by name must be denied, or be on the allowlist below with the reason it is harmless. A new table or column that looks secret fails
// here until someone decides which.
func TestRepairRoleDeniesEverythingSecret(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	denied := map[string]bool{}
	for _, tb := range db.RepairDeniedTables() {
		denied[tb] = true
	}
	// Tables whose names match but hold nothing secret.
	allowTable := map[string]string{}
	// Columns that match but are not secrets: AI token counts, a charge's session type, a milestone's dedupe key.
	allowColumn := map[string]string{
		"ai_usage.input_tokens": "a count of model tokens", "ai_usage.output_tokens": "a count of model tokens",
		"drive_efficiency.input_tokens": "a count of model tokens", "drive_efficiency.output_tokens": "a count of model tokens",
		"drive_recaps.input_tokens": "a count of model tokens", "drive_recaps.output_tokens": "a count of model tokens",
		"charges.session_type": "the kind of charging session", "milestones.key": "the milestone's name", "engagement_sends.key": "a send's dedupe key",
	}
	rows, err := d.QueryContext(ctx, `SELECT c.relname, a.attname FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped AND NOT c.relispartition ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tableRE := regexp.MustCompile(`token|secret|key|credential|session|challenge`)
	colRE := regexp.MustCompile(`token|secret|credential|password|private_key|sealed|_hash|challenge|endpoint|customer_id|code`)
	seenTable := map[string]bool{}
	for rows.Next() {
		var tb, col string
		if err := rows.Scan(&tb, &col); err != nil {
			t.Fatal(err)
		}
		if !seenTable[tb] {
			seenTable[tb] = true
			if tableRE.MatchString(tb) && !denied[tb] && allowTable[tb] == "" {
				t.Errorf("table %s looks secret by name and the repair role can read it: deny it (repairDeniedTables) or allowlist it here with a reason", tb)
			}
		}
		if colRE.MatchString(col) && !denied[tb] && allowColumn[tb+"."+col] == "" {
			t.Errorf("column %s.%s looks secret and the repair role can read it: deny the table or allowlist the column here with a reason", tb, col)
		}
	}
}

// The denylist may name a table that does not exist yet (a table is denied a release before the migration that creates it, so a rolled-back
// image that does not know the table never grants the repair role a read on it): the grants still succeed, and the name is on the list.
func TestGrantRepairWithADeniedTableNotYetCreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rivolt_repair') THEN CREATE ROLE rivolt_repair NOLOGIN; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
	// Make the database look like the one before the migration: no trip_shares.
	if _, err := d.ExecContext(ctx, `DROP TABLE IF EXISTS trip_shares`); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.GrantRepair(ctx, d); err != nil || !ok {
		t.Fatalf("GrantRepair with trip_shares absent: ok=%v err=%v", ok, err)
	}
	found := false
	for _, tb := range db.RepairDeniedTables() {
		found = found || tb == "trip_shares"
	}
	if !found {
		t.Fatal("trip_shares must be on the repair denylist before its migration ships")
	}
}
