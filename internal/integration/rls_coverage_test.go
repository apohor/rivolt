//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// Every table with a user_id column has row-level security enabled and a
// tenant policy, so a table added later without one fails here. So does every
// table keyed on vehicle_id alone, except the commented system tables.
func TestEveryUserTableHasTenantRLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	rows, err := pool.QueryContext(ctx, `
		SELECT c.relname, c.relrowsecurity,
		       EXISTS (SELECT 1 FROM pg_policies p WHERE p.schemaname = 'public' AND p.tablename = c.relname)
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN information_schema.columns k ON k.table_schema = 'public' AND k.table_name = c.relname AND k.column_name = 'user_id'
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var rls, policy bool
		if err := rows.Scan(&name, &rls, &policy); err != nil {
			t.Fatal(err)
		}
		n++
		if !rls || !policy {
			t.Errorf("%s has a user_id column but RLS enabled=%v policy=%v", name, rls, policy)
		}
	}
	if n < 20 {
		t.Errorf("only %d user_id tables found; the query is broken", n)
	}

	// Keyed on vehicle_id with no user_id: tenant data too, reached through the vehicle's owner.
	noTenant := map[string]string{
		"subscription_leases": "pod bookkeeping keyed by Rivian vehicle id, no user data",
	}
	vrows, err := pool.QueryContext(ctx, `
		SELECT c.relname, c.relrowsecurity,
		       EXISTS (SELECT 1 FROM pg_policies p WHERE p.schemaname = 'public' AND p.tablename = c.relname)
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN information_schema.columns k ON k.table_schema = 'public' AND k.table_name = c.relname AND k.column_name = 'vehicle_id'
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		   AND NOT EXISTS (SELECT 1 FROM information_schema.columns u
		                    WHERE u.table_schema = 'public' AND u.table_name = c.relname AND u.column_name = 'user_id')`)
	if err != nil {
		t.Fatal(err)
	}
	defer vrows.Close()
	sawPack := false
	for vrows.Next() {
		var name string
		var rls, policy bool
		if err := vrows.Scan(&name, &rls, &policy); err != nil {
			t.Fatal(err)
		}
		if name == "vehicle_pack_health_samples" {
			sawPack = true
		}
		if _, ok := noTenant[name]; ok {
			continue
		}
		if !rls || !policy {
			t.Errorf("%s is keyed on vehicle_id but RLS enabled=%v policy=%v", name, rls, policy)
		}
	}
	if !sawPack {
		t.Error("vehicle_pack_health_samples not found among the vehicle_id tables; the query is broken")
	}
}
