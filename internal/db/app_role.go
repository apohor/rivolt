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
//   - no view: a view runs with its owner's rights, so it would read past the policies;
//   - the sequences (for inserts) and rivolt_current_user_id(), which the policies call.
//
// Default privileges cover a table a later migration creates (so a new per-user table works on the request
// path without a grant here); the next boot's run narrows a new table without row-level security to SELECT,
// and the coverage test fails on a per-user table without a policy.

// appDeniedTables hold secrets or system state that no request handler reads directly: provider keys, the push
// signing key, the migration ledger, the pre-account waitlist and its tokens, and the system retry queue.
// Sessions and Rivian secrets have row-level security and are scoped like any per-user table.
var appDeniedTables = []string{
	"app_settings", "push_vapid", "migrations", "signup_requests", "idp_delete_failures", "subscription_leases",
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
		SELECT c.relname, c.relrowsecurity
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		 ORDER BY c.relname`)
	if err != nil {
		return true, fmt.Errorf("app role: list tables: %w", err)
	}
	type table struct {
		name string
		rls  bool
	}
	var tables []table
	for rows.Next() {
		var tb table
		if err := rows.Scan(&tb.name, &tb.rls); err != nil {
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
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, owner, role),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s`, owner, role),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, role),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION rivolt_current_user_id() TO %s`, role),
	}
	for _, tb := range tables {
		id := quoteIdent(tb.name)
		switch {
		case denied[tb.name]:
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
