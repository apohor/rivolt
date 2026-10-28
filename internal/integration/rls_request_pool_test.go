//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// Row-level security, stage 1: the request role and the request pool, against a real schema. The pool must
// scope every query to the context's user however connections are reused, and must refuse a role the tenant
// policies don't apply to.

// rlsFixture is a migrated database with two users, the request role granted, and its DSN.
type rlsFixture struct {
	owner   *sql.DB
	appDSN  string
	ownerDN string
	a, b    uuid.UUID
	role    string
}

func newRLSFixture(ctx context.Context, t *testing.T) *rlsFixture {
	t.Helper()
	dsn := startPostgres(ctx, t)
	owner, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	a, err := db.EnsureUser(ctx, owner, "rls-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.EnsureUser(ctx, owner, "rls-b")
	if err != nil {
		t.Fatal(err)
	}
	// Roles are cluster-wide and the test databases share a server: a name per test.
	role := "rivolt_app_" + randHex(t, 6)
	if _, err := owner.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'pw'`, role)); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if ok, err := db.GrantApp(ctx, owner, role); err != nil || !ok {
		t.Fatalf("GrantApp = %v, %v", ok, err)
	}
	return &rlsFixture{owner: owner, appDSN: asRole(t, dsn, role, "pw"), ownerDN: dsn, a: a, b: b, role: role}
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func asRole(t *testing.T, dsn, role, pw string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pw)
	return u.String()
}

// visible is what `SELECT id FROM users` returns on q for ctx (users' policy is on id: own row only).
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func visible(ctx context.Context, q queryer) ([]uuid.UUID, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// interleave runs n queries on d, alternating the two users and the three ways a query gets a connection
// (a plain query, a transaction, a pinned *sql.Conn), and counts those that saw anything but the caller's own
// row. Under contention: every query sleeps a little while it holds its connection.
func interleave(ctx context.Context, d *sql.DB, users [2]uuid.UUID, n int) (leaks, errs int64) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			me := users[i%2]
			uctx := auth.WithUser(ctx, me)
			var got []uuid.UUID
			var err error
			switch i % 3 {
			case 0:
				got, err = visible(uctx, d)
				if err == nil {
					_, err = d.ExecContext(uctx, `SELECT pg_sleep(0.002)`)
				}
			case 1:
				var tx *sql.Tx
				if tx, err = d.BeginTx(uctx, nil); err == nil {
					_, _ = tx.ExecContext(uctx, `SELECT pg_sleep(0.002)`)
					got, err = visible(uctx, tx)
					_ = tx.Commit()
				}
			case 2:
				var c *sql.Conn
				if c, err = d.Conn(uctx); err == nil {
					_, _ = c.ExecContext(uctx, `SELECT pg_sleep(0.002)`)
					got, err = visible(uctx, c)
					_ = c.Close()
				}
			}
			if err != nil {
				atomic.AddInt64(&errs, 1)
				return
			}
			if len(got) != 1 || got[0] != me {
				atomic.AddInt64(&leaks, 1)
			}
		}(i)
	}
	wg.Wait()
	return leaks, errs
}

// One connection, 200 interleaved requests of two users, through queries, transactions and pinned
// connections: each only ever sees itself.
func TestRequestPoolScopesEveryAcquire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	rp, err := db.OpenRequestPool(ctx, f.appDSN, 1, auth.UserFromContext)
	if err != nil {
		t.Fatalf("OpenRequestPool: %v", err)
	}
	t.Cleanup(rp.Close)
	leaks, errs := interleave(ctx, rp.DB, [2]uuid.UUID{f.a, f.b}, 200)
	if leaks != 0 || errs != 0 {
		t.Errorf("%d of 200 requests saw another user's rows, %d failed", leaks, errs)
	}
}

// The mutation proof: the same workload on a pool database/sql is allowed to manage (an open-connection limit,
// so released connections go straight to waiting queries; or idle connections, reused without pgxpool) must
// leak, or the test above couldn't tell.
func TestRequestPoolLeakIsCaughtWhenMisconfigured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	wrong := func(maxOpen, maxIdle int) *sql.DB {
		cfg, err := pgxpool.ParseConfig(f.appDSN)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns = 1
		cfg.PrepareConn = db.RequestConnHook(auth.UserFromContext)
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		d := stdlib.OpenDBFromPool(pool)
		d.SetMaxOpenConns(maxOpen)
		d.SetMaxIdleConns(maxIdle)
		t.Cleanup(func() { _ = d.Close(); pool.Close() })
		return d
	}

	// database/sql's open limit: a released connection is handed to a waiter without pgxpool.
	if leaks, _ := interleave(ctx, wrong(1, 0), [2]uuid.UUID{f.a, f.b}, 200); leaks == 0 {
		t.Error("MaxOpenConns 1: no leak seen; the interleaving test can't catch a handoff")
	}
	// database/sql's idle connections: deterministic, A then B on the same idle connection.
	d := wrong(0, 1)
	if _, err := visible(auth.WithUser(ctx, f.a), d); err != nil {
		t.Fatal(err)
	}
	if got, err := visible(auth.WithUser(ctx, f.b), d); err != nil || len(got) != 1 || got[0] != f.a {
		t.Errorf("idle reuse: B saw %v (%v); want A's row, the leak this pool exists to prevent", got, err)
	}
}

// No user in the context: nothing is visible, and nothing can be written for anyone.
func TestRequestPoolWithoutUserSeesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	rp, err := db.OpenRequestPool(ctx, f.appDSN, 2, auth.UserFromContext)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.Close)
	// A user's request first, so a reused connection would still carry A.
	if got, err := visible(auth.WithUser(ctx, f.a), rp.DB); err != nil || len(got) != 1 {
		t.Fatalf("A: %v, %v", got, err)
	}
	if got, err := visible(ctx, rp.DB); err != nil || len(got) != 0 {
		t.Errorf("no user: saw %v (%v), want nothing", got, err)
	}
	if _, err := rp.DB.ExecContext(ctx, `UPDATE users SET display_name = 'x' WHERE id = $1`, f.a); err != nil {
		t.Fatalf("update: %v", err)
	}
	var name sql.NullString
	if err := f.owner.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = $1`, f.a).Scan(&name); err != nil || name.String == "x" {
		t.Errorf("no user changed A's row (%v, %v)", name, err)
	}
	// B can't write A's row either.
	if _, err := rp.DB.ExecContext(auth.WithUser(ctx, f.b), `UPDATE users SET display_name = 'y' WHERE id = $1`, f.a); err != nil {
		t.Fatalf("update as B: %v", err)
	}
	if err := f.owner.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = $1`, f.a).Scan(&name); err != nil || name.String == "y" {
		t.Errorf("B changed A's row (%v, %v)", name, err)
	}
}

// The pool refuses a role the tenant policies don't apply to: the tables' owner and a BYPASSRLS role.
func TestRequestPoolRefusesBypassingRoles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	if rp, err := db.OpenRequestPool(ctx, f.ownerDN, 1, auth.UserFromContext); !errors.Is(err, db.ErrRequestRoleBypassesRLS) {
		if rp != nil {
			rp.Close()
		}
		t.Errorf("as the owner: err %v, want ErrRequestRoleBypassesRLS", err)
	}
	bypass := "rivolt_bypass_" + randHex(t, 6)
	if _, err := f.owner.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN BYPASSRLS PASSWORD 'pw'`, bypass)); err != nil {
		t.Fatal(err)
	}
	if rp, err := db.OpenRequestPool(ctx, asRole(t, f.ownerDN, bypass, "pw"), 1, auth.UserFromContext); !errors.Is(err, db.ErrRequestRoleBypassesRLS) {
		if rp != nil {
			rp.Close()
		}
		t.Errorf("as a BYPASSRLS role: err %v, want ErrRequestRoleBypassesRLS", err)
	}
	// A member of the owner role inherits its rights (and its ownership): refused too.
	var owner string
	if err := f.owner.QueryRowContext(ctx, `SELECT current_user`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	member := "rivolt_member_" + randHex(t, 6)
	for _, q := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'pw'`, member),
		fmt.Sprintf(`GRANT %s TO %s`, owner, member),
	} {
		if _, err := f.owner.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if rp, err := db.OpenRequestPool(ctx, asRole(t, f.ownerDN, member, "pw"), 1, auth.UserFromContext); !errors.Is(err, db.ErrRequestRoleBypassesRLS) {
		if rp != nil {
			rp.Close()
		}
		t.Errorf("as a member of the owner role: err %v, want ErrRequestRoleBypassesRLS", err)
	}
	rp, err := db.OpenRequestPool(ctx, f.appDSN, 1, auth.UserFromContext)
	if err != nil {
		t.Fatalf("as the request role: %v", err)
	}
	rp.Close()
}

// The request role's grants: full DML on every table with row-level security, SELECT only on the other plain
// tables, nothing on the denied ones, views, materialized views or partitions (made before or after the grants),
// EXECUTE on the policy function, and no access to a later table until GrantApp runs again.
func TestGrantAppPrivileges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	if ok, err := db.GrantApp(ctx, f.owner, "rivolt_app_missing"); err != nil || ok {
		t.Errorf("missing role: %v, %v; want false, nil", ok, err)
	}
	if _, err := db.GrantApp(ctx, f.owner, "bad;name"); err == nil {
		t.Error("invalid role name accepted")
	}
	if ok, err := db.GrantApp(ctx, f.owner, f.role); err != nil || !ok { // idempotent
		t.Fatalf("second run: %v, %v", ok, err)
	}
	// priv reports whether the role holds every privilege in ps (has_table_privilege with a list is true if any
	// one is held, so each is asked on its own).
	priv := func(table, ps string) bool {
		t.Helper()
		for _, p := range strings.Split(ps, ",") {
			var ok bool
			if err := f.owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, f.role, table, strings.TrimSpace(p)).Scan(&ok); err != nil {
				t.Fatalf("has_table_privilege(%s, %s): %v", table, p, err)
			}
			if !ok {
				return false
			}
		}
		return true
	}
	rows, err := f.owner.QueryContext(ctx, `
		SELECT c.relname, c.relrowsecurity, CASE WHEN c.relispartition THEN 'partition' ELSE c.relkind::text END
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm')`)
	if err != nil {
		t.Fatal(err)
	}
	type rel struct {
		name string
		rls  bool
		kind string
	}
	var rels []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.name, &r.rls, &r.kind); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, r)
	}
	rows.Close()
	denied := map[string]bool{"app_settings": true, "push_vapid": true, "migrations": true, "signup_requests": true, "idp_delete_failures": true, "subscription_leases": true, "account_leases": true, "lease_pods": true}
	nRLS := 0
	for _, r := range rels {
		switch {
		case r.kind == "v" || r.kind == "m" || r.kind == "partition":
			if priv(r.name, "SELECT") || priv(r.name, "INSERT") {
				t.Errorf("%s %s is accessible: it reads past the policies (a view runs as its owner; a partition queried directly has none)", r.kind, r.name)
			}
		case denied[r.name]:
			if priv(r.name, "SELECT") || priv(r.name, "INSERT") {
				t.Errorf("%s is denied but accessible", r.name)
			}
		case r.rls:
			nRLS++
			if !priv(r.name, "SELECT, INSERT, UPDATE, DELETE") {
				t.Errorf("%s has row-level security but the request role can't use it", r.name)
			}
		default:
			if !priv(r.name, "SELECT") || priv(r.name, "INSERT") || priv(r.name, "DELETE") {
				t.Errorf("%s (no row-level security): want SELECT only", r.name)
			}
		}
	}
	if nRLS < 20 {
		t.Errorf("only %d tables with row-level security; the query is broken", nRLS)
	}
	var fn bool
	if err := f.owner.QueryRowContext(ctx, `SELECT has_function_privilege($1, 'rivolt_current_user_id()', 'EXECUTE')`, f.role).Scan(&fn); err != nil || !fn {
		t.Errorf("rivolt_current_user_id(): EXECUTE %v (%v)", fn, err)
	}
	for _, v := range []string{"activated", "user_rivian_connected"} {
		if priv(v, "SELECT") {
			t.Errorf("view %s is readable by the request role", v)
		}
	}

	// Later relations, created by the owner after the grants (as a migration or 0070's partition guard would):
	// none is reachable until GrantApp runs again (no default privileges on tables), and then a per-user table
	// gets DML while its partitions and a new view stay closed.
	for _, q := range []string{
		`CREATE TABLE later_per_user (id serial PRIMARY KEY, user_id uuid NOT NULL)`,
		`ALTER TABLE later_per_user ENABLE ROW LEVEL SECURITY`,
		`CREATE TABLE later_parted (user_id uuid NOT NULL, at date NOT NULL) PARTITION BY RANGE (at)`,
		`ALTER TABLE later_parted ENABLE ROW LEVEL SECURITY`,
		`CREATE TABLE later_parted_2030_01 PARTITION OF later_parted FOR VALUES FROM ('2030-01-01') TO ('2030-02-01')`,
		`CREATE VIEW later_view AS SELECT user_id FROM later_per_user`,
	} {
		if _, err := f.owner.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, r := range []string{"later_per_user", "later_parted", "later_parted_2030_01", "later_view"} {
		if priv(r, "SELECT") || priv(r, "INSERT") {
			t.Errorf("%s: reachable before GrantApp ran again (default privileges?)", r)
		}
	}
	if ok, err := db.GrantApp(ctx, f.owner, f.role); err != nil || !ok {
		t.Fatalf("GrantApp after the new tables: %v, %v", ok, err)
	}
	// A partition made after this run too (the runtime monthly guard): still closed.
	if _, err := f.owner.ExecContext(ctx, `CREATE TABLE later_parted_2030_02 PARTITION OF later_parted FOR VALUES FROM ('2030-02-01') TO ('2030-03-01')`); err != nil {
		t.Fatal(err)
	}
	if !priv("later_per_user", "SELECT, INSERT, UPDATE, DELETE") || !priv("later_parted", "SELECT, INSERT, UPDATE, DELETE") {
		t.Error("after GrantApp: a new per-user table (or partitioned parent) isn't granted")
	}
	for _, r := range []string{"later_parted_2030_01", "later_parted_2030_02", "later_view"} {
		if priv(r, "SELECT") || priv(r, "INSERT") {
			t.Errorf("%s is accessible to the request role", r)
		}
	}
}

// A member of the owner role through NOINHERIT does not inherit its rights, but can SET ROLE to it: refused too (DA's nit on
// #1003: pg_has_role MEMBER, not USAGE).
func TestRequestPoolRefusesNoInheritMemberOfOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	var owner string
	if err := f.owner.QueryRowContext(ctx, `SELECT current_user`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	m := "rivolt_noinherit_" + randHex(t, 6)
	for _, q := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN NOINHERIT PASSWORD 'pw'`, m),
		fmt.Sprintf(`GRANT %s TO %s`, owner, m),
	} {
		if _, err := f.owner.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if rp, err := db.OpenRequestPool(ctx, asRole(t, f.ownerDN, m, "pw"), 1, auth.UserFromContext); !errors.Is(err, db.ErrRequestRoleBypassesRLS) {
		if rp != nil {
			rp.Close()
		}
		t.Errorf("as a NOINHERIT member of the owner role: err %v, want ErrRequestRoleBypassesRLS", err)
	}
}

// The no-user counter fires once per connection handed out for a context without a user, and not for one with a user.
func TestRequestPoolCountsAcquiresWithoutUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	var n atomic.Int64
	rp, err := db.OpenRequestPool(ctx, f.appDSN, 1, auth.UserFromContext, db.WithNoUserCounter(func(context.Context) { n.Add(1) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.Close)
	base := n.Load() // the role check at open ran without a user
	if _, err := rp.DB.ExecContext(auth.WithUser(ctx, f.a), `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	if got := n.Load() - base; got != 0 {
		t.Errorf("a query with a user counted %d no-user acquires", got)
	}
	if _, err := rp.DB.ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	if got := n.Load() - base; got != 1 {
		t.Errorf("a query without a user counted %d, want 1", got)
	}
}

// PrepareRequestRole grants synchronously: a table created after the first grants is reachable the moment it returns, and a
// role that does not exist yet is ErrRequestRoleMissing (so the caller picks between waiting and refusing).
func TestPrepareRequestRoleGrantsALaterTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newRLSFixture(ctx, t)
	if err := db.PrepareRequestRole(ctx, f.owner, "rivolt_app_missing"); !errors.Is(err, db.ErrRequestRoleMissing) {
		t.Errorf("missing role: %v, want ErrRequestRoleMissing", err)
	}
	if ok, err := db.GrantApp(ctx, f.owner, f.role); err != nil || !ok {
		t.Fatalf("first grants: %v %v", ok, err)
	}
	for _, q := range []string{
		`CREATE TABLE prep_later (id serial PRIMARY KEY, user_id uuid NOT NULL)`,
		`ALTER TABLE prep_later ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY tenant ON prep_later USING (user_id = rivolt_current_user_id()) WITH CHECK (user_id = rivolt_current_user_id())`,
	} {
		if _, err := f.owner.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	can := func() bool {
		var ok bool
		if err := f.owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, 'prep_later', 'INSERT')`, f.role).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if can() {
		t.Fatal("the role has access to a table it was never granted")
	}
	if err := db.PrepareRequestRole(ctx, f.owner, f.role); err != nil {
		t.Fatal(err)
	}
	if !can() {
		t.Error("PrepareRequestRole returned but the new table is not granted")
	}
}
