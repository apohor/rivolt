//go:build integration

package drain

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The fleet scan runs with a raised work_mem for that transaction only: inside
// it the setting is computeWorkMem, and on the same connection afterwards it is back to
// what it was. (The leader's advisory lock, which is session-level, survives.)
func TestComputeRunsWithRaisedWorkMemForOneTransaction(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	show := func(q Querier) string {
		t.Helper()
		rows, err := q.QueryContext(ctx, `SHOW work_mem`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var v string
		if rows.Next() {
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
		}
		return v
	}
	before := show(conn)
	var during string
	if err := inWorkMem(ctx, conn, func(q Querier) error { during = show(q); return nil }); err != nil {
		t.Fatal(err)
	}
	if during != computeWorkMem {
		t.Errorf("work_mem inside the scan = %q, want %q", during, computeWorkMem)
	}
	if after := show(conn); after != before {
		t.Errorf("work_mem after = %q, before = %q: SET LOCAL leaked past the transaction", after, before)
	}
	// An error in the scan rolls the transaction back and leaves the connection usable.
	if err := inWorkMem(ctx, conn, func(Querier) error { return context.DeadlineExceeded }); err == nil {
		t.Error("an error from the scan was swallowed")
	}
	if after := show(conn); after != before {
		t.Errorf("work_mem after a failed scan = %q, want %q", after, before)
	}
}
