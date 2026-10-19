//go:build integration

package samples_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/testpg"
)

// The partition helper refuses months outside 2018-01 .. now + 13 months (the
// database's own bound, behind the Go writers'), restores lock_timeout, and a
// batch with a far date is refused whole by the store without making partitions.
func TestPartitionHelperAndStoreRefuseUnreasonableMonths(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	partitions := func() int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM pg_class WHERE relname LIKE 'vehicle_state_y%' AND relkind = 'r'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := partitions()

	for name, when := range map[string]time.Time{
		"year 1":             time.Date(1, 6, 1, 0, 0, 0, 0, time.UTC),
		"2017":               time.Date(2017, 12, 1, 0, 0, 0, 0, time.UTC),
		"year 9999":          time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		"two years from now": time.Now().AddDate(2, 0, 0),
	} {
		var name2 string
		err := pool.QueryRowContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, when).Scan(&name2)
		if err == nil || !strings.Contains(err.Error(), "outside the accepted range") {
			t.Errorf("%s: %v (%s), want the range error", name, err, name2)
		}
	}
	if got := partitions(); got != before {
		t.Errorf("partitions %d -> %d after refused months", before, got)
	}
	// An accepted month works, and lock_timeout is the session's own afterwards.
	var timeoutBefore, timeoutAfter, part string
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.QueryRowContext(ctx, `SHOW lock_timeout`).Scan(&timeoutBefore); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 5, 0)
	if err := conn.QueryRowContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, when).Scan(&part); err != nil || part == "" {
		t.Fatalf("an in-range month: %q %v", part, err)
	}
	if err := conn.QueryRowContext(ctx, `SHOW lock_timeout`).Scan(&timeoutAfter); err != nil || timeoutAfter != timeoutBefore {
		t.Errorf("lock_timeout %q -> %q: not restored (%v)", timeoutBefore, timeoutAfter, err)
	}

	mid := partitions()

	// The store refuses a batch with a far date before touching anything.
	uid, err := db.EnsureUser(ctx, pool, "window-a")
	if err != nil {
		t.Fatal(err)
	}
	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	batch := []samples.Sample{
		{VehicleID: "rv-window", At: time.Now(), Source: "test"},
		{VehicleID: "rv-window", At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), Source: "test"},
	}
	for name, run := range map[string]func() error{
		"InsertBatch": func() error { return store.InsertBatch(ctx, batch) },
		"InsertBulk":  func() error { return store.InsertBulk(ctx, batch) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "outside the accepted window") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := partitions(); got != mid {
		t.Errorf("partitions %d, want %d: a refused batch made one", got, mid)
	}
}
