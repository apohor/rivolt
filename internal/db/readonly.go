package db

import (
	"context"
	"database/sql"
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
	for _, s := range stmts {
		if _, err := d.ExecContext(ctx, s); err != nil {
			return true, fmt.Errorf("readonly grant %q: %w", s, err)
		}
	}
	return true, nil
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
