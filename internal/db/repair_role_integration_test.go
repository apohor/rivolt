//go:build integration

package db_test

import (
	"context"
	"reflect"
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
