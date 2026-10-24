//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// N26: the reporting role reads pg_stat_statements only through rivolt_query_stats(), which the readonly grants
// create once the extension exists: SECURITY DEFINER, owned by the app's role, EXECUTE for the reporting role
// and nobody else. (The test server doesn't preload the library, so the function is checked, not called.)
func TestQueryStatsFunction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	role := "qs_ro_" + hex.EncodeToString(b) // roles are cluster-wide; the test servers are shared
	other := "qs_other_" + hex.EncodeToString(b)
	for _, r := range []string{role, other} {
		if _, err := pool.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s NOLOGIN`, r)); err != nil {
			t.Fatal(err)
		}
	}
	exists := func() bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRowContext(ctx, `SELECT to_regprocedure('rivolt_query_stats()') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// No extension: the grants still apply and no function appears.
	if ok, err := db.GrantReadonly(ctx, pool, role); err != nil || !ok {
		t.Fatalf("GrantReadonly without the extension: %v, %v", ok, err)
	}
	if exists() {
		t.Fatal("rivolt_query_stats() created without pg_stat_statements")
	}

	if _, err := pool.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pg_stat_statements`); err != nil {
		t.Fatalf("create extension: %v", err)
	}
	for i := 0; i < 2; i++ { // idempotent: the boot loop re-runs it
		if ok, err := db.GrantReadonly(ctx, pool, role); err != nil || !ok {
			t.Fatalf("GrantReadonly run %d with the extension: %v, %v", i+1, ok, err)
		}
	}
	if !exists() {
		t.Fatal("rivolt_query_stats() not created")
	}
	var definer, ownerIsApp, roleExec, otherExec, publicExec bool
	if err := pool.QueryRowContext(ctx, `
		SELECT p.prosecdef, pg_get_userbyid(p.proowner) = current_user,
		       has_function_privilege($1, p.oid, 'EXECUTE'),
		       has_function_privilege($2, p.oid, 'EXECUTE'),
		       EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')
		  FROM pg_proc p WHERE p.oid = to_regprocedure('rivolt_query_stats()')`, role, other,
	).Scan(&definer, &ownerIsApp, &roleExec, &otherExec, &publicExec); err != nil {
		t.Fatal(err)
	}
	if !definer || !ownerIsApp {
		t.Errorf("rivolt_query_stats(): SECURITY DEFINER %v, owned by the app's role %v; want both", definer, ownerIsApp)
	}
	if !roleExec || otherExec || publicExec {
		t.Errorf("EXECUTE: reporting role %v, another role %v, PUBLIC %v; want only the reporting role", roleExec, otherExec, publicExec)
	}

	// The definer's resolution can't be steered by a caller's temp table: pg_temp is listed last in its
	// search_path, and every relation it reads is named with its schema.
	var def string
	if err := pool.QueryRowContext(ctx, `SELECT pg_get_functiondef(to_regprocedure('rivolt_query_stats()'))`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`SET search_path TO 'pg_catalog', 'public', 'pg_temp'`,
		`public.pg_stat_statements`,
		`pg_catalog.pg_roles`,
		`pg_catalog.pg_database`,
	} {
		if !strings.Contains(def, want) {
			t.Errorf("rivolt_query_stats() definition lacks %q:\n%s", want, def)
		}
	}
}
