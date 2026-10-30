package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// Read-only reporting role (Grafana's Postgres datasource).
//
// The role itself is created by the CNPG operator (managed role with
// BYPASSRLS, so per-user tables aren't empty to it); the app only owns
// which tables it may read. That split is deliberate: the app role
// can't create roles, and CNPG creates them asynchronously, so a
// one-shot migration could run before the role exists and silently
// grant nothing. GrantReadonly is idempotent and is re-run at boot and
// periodically instead.
//
// The list is an explicit allowlist. Left out on purpose:
// user_secrets (encrypted Rivian sessions), sessions (token hashes),
// push_subscriptions / push_vapid (push keys), app_settings (AI provider
// keys), user_settings, migrations, and signup_requests.signup_token.
// A table added later is not readable until it is listed here.
var readonlyTables = []string{
	"users", "vehicles", "drives", "charges", "vehicle_state", "imports",
	"flags", "subscription_leases", "ai_usage", "ai_call_usage",
	"drive_recaps", "drive_efficiency", "drive_weather",
	"vehicle_pack_health_samples", "saved_trips", "locations",
	// Fleet bookkeeping of sample thinning (migration 0072), for the
	// storage dashboard; no user columns.
	"sample_tiers",
	// A view: a has-a-Rivian-session boolean per user, never the
	// session itself (migration 0052).
	"user_rivian_connected",
}

// readonlyColumns grants a column subset where the table holds a secret.
var readonlyColumns = map[string][]string{
	"signup_requests": {"id", "email", "message", "status", "decided_by",
		"decided_at", "requested_at", "token_expires_at", "token_used_at"},
}

var roleName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// GrantReadonly grants SELECT on the allowlist to role. It returns
// (false, nil) without doing anything when the role doesn't exist yet,
// so callers can retry later.
func GrantReadonly(ctx context.Context, d *sql.DB, role string) (bool, error) {
	if !roleName.MatchString(role) {
		return false, fmt.Errorf("readonly role %q: invalid name", role)
	}
	var exists bool
	if err := d.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("readonly role lookup: %w", err)
	}
	if !exists {
		return false, nil
	}
	// The name is validated above and can't be a bind parameter in DDL.
	stmts := []string{fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role)}
	for _, t := range readonlyTables {
		stmts = append(stmts, fmt.Sprintf(`GRANT SELECT ON %s TO %s`, t, role))
	}
	for t, cols := range readonlyColumns {
		stmts = append(stmts, fmt.Sprintf(`GRANT SELECT (%s) ON %s TO %s`,
			strings.Join(cols, ", "), t, role))
	}
	// One replica at a time (grant_lock.go): the grants and the query-stats function's
	// CREATE OR REPLACE run on the connection holding the lock.
	if err := withGrantLock(ctx, d, func(q grantExec) error {
		for _, s := range stmts {
			if _, err := q.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("readonly grant %q: %w", s, err)
			}
		}
		return ensureQueryStats(ctx, q, role)
	}); err != nil {
		return true, err
	}
	return true, nil
}

// queryStatsFunc is how the reporting role reads pg_stat_statements (N26): only the normalized text (literals as
// $1, never values) and counters of the statements this app's role ran in this database. SECURITY DEFINER and
// owned by the app's role, so it sees that role's own query text without the reporting role holding
// pg_read_all_stats, which would also show other sessions' pg_stat_activity text with literals in it (DA,
// Oct 24). Inside the function current_user is the owner, so the filter is the app's own statements.
//
// "Normalized only" relies on pg_stat_statements.track_utility = off (set with the library in rivolt-infra):
// utility statements (SET, DDL, ALTER ROLE … PASSWORD) are stored as written, literals included, when tracked.
// As a definer, the function names every relation with its schema and lists pg_temp last in its search_path,
// so no temp table of the caller's can stand in for one of them. %s is the extension's schema, quoted.
const queryStatsFunc = `
CREATE OR REPLACE FUNCTION public.rivolt_query_stats()
RETURNS TABLE (queryid bigint, query text, calls bigint, total_exec_time double precision,
               mean_exec_time double precision, rows bigint, shared_blks_hit bigint,
               shared_blks_read bigint, temp_blks_written bigint)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
	SELECT s.queryid, s.query, s.calls, s.total_exec_time, s.mean_exec_time, s.rows,
	       s.shared_blks_hit, s.shared_blks_read, s.temp_blks_written
	  FROM %s.pg_stat_statements s
	 WHERE s.userid = (SELECT r.oid FROM pg_catalog.pg_roles r WHERE r.rolname = current_user)
	   AND s.dbid = (SELECT d.oid FROM pg_catalog.pg_database d WHERE d.datname = pg_catalog.current_database())
$$`

// ensureQueryStats creates rivolt_query_stats() for role when the pg_stat_statements extension exists (CNPG
// creates it once the library is preloaded); without it, nothing. Idempotent, like the grants around it.
func ensureQueryStats(ctx context.Context, d grantExec, role string) error {
	var schema string
	err := d.QueryRowContext(ctx,
		`SELECT pg_catalog.quote_ident(n.nspname) FROM pg_catalog.pg_extension e
		   JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
		  WHERE e.extname = 'pg_stat_statements'`,
	).Scan(&schema)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("query stats: extension lookup: %w", err)
	}
	for _, s := range []string{
		fmt.Sprintf(queryStatsFunc, schema),
		`REVOKE ALL ON FUNCTION public.rivolt_query_stats() FROM PUBLIC`,
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION public.rivolt_query_stats() TO %s`, role),
	} {
		if _, err := d.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("query stats: %w", err)
		}
	}
	return nil
}

// EnsureReadonlyGrants is GrantReadonly with logging, for the boot and
// periodic callers. A missing role is logged once at info, not as an
// error: on preview or a fresh install the role may legitimately not
// exist.
func EnsureReadonlyGrants(ctx context.Context, d *sql.DB, role string, logger *slog.Logger) {
	if d == nil || role == "" {
		return
	}
	ok, err := GrantReadonly(ctx, d, role)
	switch {
	case err != nil:
		logger.Warn("readonly grants failed", "role", role, "err", err.Error())
	case !ok:
		logger.Info("readonly role not present yet; grants skipped", "role", role)
	default:
		logger.Debug("readonly grants applied", "role", role, "tables", len(readonlyTables)+len(readonlyColumns))
	}
}
