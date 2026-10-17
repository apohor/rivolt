//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// Every per-user table added since the first RLS migration has row-level
// security enabled and a tenant-isolation policy on user_id.
func TestNewTablesHaveTenantRLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	for _, tbl := range []string{"rivian_receipts", "rivian_receipt_sync", "ota_notices", "vehicle_leases"} {
		var enabled bool
		if err := pool.QueryRowContext(ctx, `SELECT relrowsecurity FROM pg_class WHERE relname = $1 AND relkind = 'r'`, tbl).Scan(&enabled); err != nil || !enabled {
			t.Errorf("%s: row level security enabled = %v, %v", tbl, enabled, err)
		}
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM pg_policies WHERE tablename = $1 AND policyname = $2`, tbl, tbl+"_tenant_isolation").Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: tenant policy count = %d, %v", tbl, n, err)
		}
	}
}
