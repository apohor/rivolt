package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// RepairRole is the database role the repair CLIs use when they are run as an operator (single-owner M4-4). The name is fixed: the migration's
// triggers (0108) fire on writes by this role and no other. CNPG creates it (LOGIN, BYPASSRLS: a repair reaches every user's rows); the
// server grants it what it needs when it next runs, like the request and reporting roles.
const RepairRole = "rivolt_repair"

// repairWriteTables are the tables the repairs write. Each has the operator guard trigger (migration 0108): a write by the repair role needs
// an open operator action. Keep this list and the migration's in step (TestRepairGuardTablesMatchTheGrants).
var repairWriteTables = []string{"charges", "drives", "charges_capped_backup", "charges_deleted_backup", "vehicles"}

// RepairDeniedTables is the denylist, for tests.
func RepairDeniedTables() []string { return append([]string(nil), repairDeniedTables...) }

// RepairWriteTables is the list above, for tests.
func RepairWriteTables() []string { return append([]string(nil), repairWriteTables...) }

// repairDeniedTables are the tables the repair role cannot read: every table that holds a token, a key, a credential or an identifier a repair
// never needs (the read grants are a denylist, DA M4-4). operator_actions is reached only through the begin and end functions. users stays
// readable: reprice joins it, and the role is operator-only. TestRepairRoleDeniesEverythingSecret keeps this list complete.
var repairDeniedTables = append([]string{
	"user_secrets", "sessions", "archive_keys", "invite_codes", "operator_actions",
	"webauthn_credentials", "webauthn_ceremonies", "webauthn_users", // passkeys and their challenge state
	"push_subscriptions", "push_apns_devices", "push_apns_activities", // web-push endpoints and keys, APNs device and Live Activity tokens
	"app_auth_requests", "apple_link_intents", "apple_identities", "apple_events", "apple_notification_seen", // the iPhone sign-in and Apple linking state
	"billing_customers", "billing_stripe_subs", "billing_apple_subs", "stripe_events", "stripe_closed_customers", // payment-provider identifiers
}, appDeniedTables...)

// GrantRepair applies the repair role's grants. It returns (false, nil) when the role does not exist yet, so a caller can retry. Idempotent.
//   - SELECT on every table except repairDeniedTables (a repair reads widely: vehicle_state, receipts, restamps, pack health);
//   - INSERT, UPDATE and DELETE on repairWriteTables only (the guard trigger then requires an open operator action);
//   - EXECUTE on rivolt_operator_begin and rivolt_operator_end, the only way to open and close an action.
func GrantRepair(ctx context.Context, d *sql.DB) (bool, error) {
	var exists bool
	if err := d.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, RepairRole).Scan(&exists); err != nil {
		return false, fmt.Errorf("repair role lookup: %w", err)
	}
	if !exists {
		return false, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT c.relname, c.relkind IN ('v', 'm') OR c.relispartition
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm')
		 ORDER BY c.relname`)
	if err != nil {
		return true, fmt.Errorf("repair role: list tables: %w", err)
	}
	type table struct {
		name string
		none bool // a view, materialized view or partition: reached through its parent, not directly
	}
	var tables []table
	for rows.Next() {
		var tb table
		if err := rows.Scan(&tb.name, &tb.none); err != nil {
			rows.Close()
			return true, fmt.Errorf("repair role: list tables: %w", err)
		}
		tables = append(tables, tb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return true, fmt.Errorf("repair role: list tables: %w", err)
	}
	denied := map[string]bool{}
	for _, t := range repairDeniedTables {
		denied[t] = true
	}
	write := map[string]bool{}
	for _, t := range repairWriteTables {
		write[t] = true
	}
	stmts := []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, RepairRole),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, RepairRole),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION rivolt_operator_begin(text, text, text, text, text) TO %s`, RepairRole),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION rivolt_operator_end(uuid, text) TO %s`, RepairRole),
	}
	for _, tb := range tables {
		id := quoteIdent(tb.name)
		switch {
		case denied[tb.name] || tb.none:
			stmts = append(stmts, fmt.Sprintf(`REVOKE ALL ON %s FROM %s`, id, RepairRole))
		case write[tb.name]:
			stmts = append(stmts, fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO %s`, id, RepairRole))
		default:
			stmts = append(stmts,
				fmt.Sprintf(`REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON %s FROM %s`, id, RepairRole),
				fmt.Sprintf(`GRANT SELECT ON %s TO %s`, id, RepairRole))
		}
	}
	if err := withGrantLock(ctx, d, "repair", func(q grantExec) error {
		for _, s := range stmts {
			if _, err := q.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("repair grant %q: %w", s, err)
			}
		}
		return nil
	}); err != nil {
		return true, err
	}
	return true, nil
}

// EnsureRepairGrants is GrantRepair with logging, for the boot and periodic callers. A missing role is normal until the infra creates it.
func EnsureRepairGrants(ctx context.Context, d *sql.DB, logger *slog.Logger) {
	if d == nil {
		return
	}
	ok, err := GrantRepair(ctx, d)
	switch {
	case err != nil:
		logger.Warn("repair role grants failed", "err", err.Error())
	case !ok:
		logger.Info("repair role not present yet; grants skipped", "role", RepairRole)
	default:
		logger.Debug("repair role grants applied", "role", RepairRole)
	}
}
