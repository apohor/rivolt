package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// The request role (row-level security, stage 2; design in the team's RLS activation note, approved by DA on
// Oct 23). Request handling will connect as this role instead of the tables' owner, so the tenant policies of
// migrations 0008/0059/0060 apply to it. Like the reporting role, CNPG creates it (a managed role, LOGIN, no
// BYPASSRLS) and the app only owns its grants, re-applied at boot because the role can appear later.
//
// What the role may touch:
//   - a table with row-level security (every per-user table): SELECT, INSERT, UPDATE and DELETE, which the
//     tenant policy narrows to the request's own rows;
//   - any other table: SELECT only, except appDeniedTables, which it can't read at all;
//   - no view or materialized view: a view runs with its owner's rights, so it would read past the policies;
//   - no partition: a partition doesn't carry its parent's policies when queried directly, so the role reaches
//     partitioned data only through the parent (vehicle_state's monthly partitions, created at runtime);
//   - the sequences (for inserts) and rivolt_current_user_id(), which the policies call.
//
// No default privileges on tables: they would also reach every later partition and view, which this loop would
// then have to catch. A table a later migration creates gets its grant when GrantApp next runs; stage 2 runs it
// synchronously after migrations, before it opens the request pool. Default privileges stay for sequences.

// appDeniedTables hold secrets or system state that no request handler reads directly: provider keys, the push
// signing key, the migration ledger, the pre-account waitlist and its tokens, and the system retry queue.
// Sessions and Rivian secrets have row-level security and are scoped like any per-user table.
var appDeniedTables = []string{
	"app_settings", "push_vapid", "migrations", "signup_requests", "idp_delete_failures", "subscription_leases",
	"account_leases", "lease_pods",
}

// GrantApp applies the request role's grants. It returns (false, nil) without doing anything when the role
// doesn't exist yet, so callers can retry later. Idempotent.
func GrantApp(ctx context.Context, d *sql.DB, role string) (bool, error) {
	if !roleName.MatchString(role) {
		return false, fmt.Errorf("app role %q: invalid name", role)
	}
	var exists bool
	if err := d.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("app role lookup: %w", err)
	}
	if !exists {
		return false, nil
	}
	var owner string
	if err := d.QueryRowContext(ctx, `SELECT current_user`).Scan(&owner); err != nil {
		return true, fmt.Errorf("app role: current user: %w", err)
	}
	if !roleName.MatchString(owner) {
		return true, fmt.Errorf("app role: owner %q: unexpected name", owner)
	}
	rows, err := d.QueryContext(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relkind IN ('v', 'm') OR c.relispartition
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm')
		 ORDER BY c.relname`)
	if err != nil {
		return true, fmt.Errorf("app role: list tables: %w", err)
	}
	type table struct {
		name string
		rls  bool
		none bool // a view, materialized view or partition: no access at all
	}
	var tables []table
	for rows.Next() {
		var tb table
		if err := rows.Scan(&tb.name, &tb.rls, &tb.none); err != nil {
			rows.Close()
			return true, fmt.Errorf("app role: list tables: %w", err)
		}
		tables = append(tables, tb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return true, fmt.Errorf("app role: list tables: %w", err)
	}
	denied := map[string]bool{}
	for _, t := range appDeniedTables {
		denied[t] = true
	}

	// Role names are validated above and table names come from pg_class, double-quoted: neither can be a bind
	// parameter in DDL.
	stmts := []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public REVOKE ALL ON TABLES FROM %s`, owner, role),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s`, owner, role),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, role),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION rivolt_current_user_id() TO %s`, role),
	}
	for _, tb := range tables {
		id := quoteIdent(tb.name)
		switch {
		case denied[tb.name] || tb.none:
			stmts = append(stmts, fmt.Sprintf(`REVOKE ALL ON %s FROM %s`, id, role))
		case tb.rls:
			stmts = append(stmts, fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO %s`, id, role))
		default:
			stmts = append(stmts,
				fmt.Sprintf(`REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON %s FROM %s`, id, role),
				fmt.Sprintf(`GRANT SELECT ON %s TO %s`, id, role))
		}
	}
	for _, s := range stmts {
		if _, err := d.ExecContext(ctx, s); err != nil {
			return true, fmt.Errorf("app grant %q: %w", s, err)
		}
	}
	return true, nil
}

// EnsureAppGrants is GrantApp with logging, for the boot and periodic callers. A missing role is logged at
// info: it's normal until CNPG has created it, and on installs that don't use a request role.
func EnsureAppGrants(ctx context.Context, d *sql.DB, role string, logger *slog.Logger) {
	if d == nil || role == "" {
		return
	}
	ok, err := GrantApp(ctx, d, role)
	switch {
	case err != nil:
		logger.Warn("app role grants failed", "role", role, "err", err.Error())
	case !ok:
		logger.Info("app role not present yet; grants skipped", "role", role)
	default:
		logger.Debug("app role grants applied", "role", role)
	}
}

// quoteIdent double-quotes a Postgres identifier.
func quoteIdent(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, s[i])
	}
	return string(append(out, '"'))
}

// ErrRequestRoleMissing means the request role does not exist yet (CNPG creates managed roles on its own schedule).
var ErrRequestRoleMissing = fmt.Errorf("the request role does not exist")

// PrepareRequestRole applies the request role's grants synchronously: call it after the migrations have run and before the
// request pool opens, so a table a migration just created is reachable the moment requests start. (The periodic loop in main only
// catches later changes, such as a partition created at runtime.) ErrRequestRoleMissing when the role is not there, so the caller
// chooses between waiting (the migration job warns) and refusing to start (the server, when the request pool is on).
func PrepareRequestRole(ctx context.Context, owner *sql.DB, role string) error {
	ok, err := GrantApp(ctx, owner, role)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %q", ErrRequestRoleMissing, role)
	}
	return nil
}
