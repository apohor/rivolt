// Package operator is the repair CLIs' audited path (single-owner M4-4). A mutating run as the repair role opens an operator action (a row in
// operator_actions, with a role label and a reason, never a name), connects with that action's id as the session setting
// rivolt.operator_action, and closes the action with its outcome. The database trigger (migration 0108) refuses every write by the
// repair role that does not carry an open action, so a run that skipped this fails at its first write instead of leaving no trace.
//
// The environment decides the path:
//
//	RIVOLT_REPAIR_DATABASE_URL  the repair role's DSN. Set: the audited path. Unset: the legacy path (DATABASE_URL, no audit row) with a warning,
//	                            unless RIVOLT_REPAIR_REQUIRE_ROLE=1, which refuses it.
//	RIVOLT_OPERATOR_ROLE        lead or owner (the audit row's label); required on the audited path when mutating
//	RIVOLT_REPAIR_REASON        why this run (at most 300 characters); required on the audited path when mutating
//
// A dry run needs no audit row (it reads), and uses the repair role's DSN when one is set.
package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/apohor/rivolt/internal/db"
)

// Environment variable names.
const (
	EnvDSN         = "RIVOLT_REPAIR_DATABASE_URL"
	EnvRole        = "RIVOLT_OPERATOR_ROLE"
	EnvReason      = "RIVOLT_REPAIR_REASON"
	EnvRequireRole = "RIVOLT_REPAIR_REQUIRE_ROLE"
)

// Run is an open repair run. Done closes the audit row (a no-op on the legacy path and on a dry run).
type Run struct {
	Pool *sql.DB
	done func(runErr error) error
}

// Done closes the run with the error it ended with (nil: ok). The row count is the database's own (the guard counts each row written), so
// the CLI does not report one. Call it once.
func (r *Run) Done(runErr error) error {
	if r.done == nil {
		return nil
	}
	return r.done(runErr)
}

// Open returns the pool a repair CLI works on. mutate is true for --apply (and a restore's --apply). legacyDSN is the DSN of today's path.
// warn receives the legacy-path warning (the CLI's stderr).
func Open(ctx context.Context, tool string, mutate bool, getenv func(string) string, legacyDSN string, warn io.Writer) (*Run, error) {
	dsn := strings.TrimSpace(getenv(EnvDSN))
	if dsn == "" {
		if mutate && getenv(EnvRequireRole) == "1" {
			return nil, fmt.Errorf("%s=1: a mutating run must use the repair role (set %s)", EnvRequireRole, EnvDSN)
		}
		if mutate {
			fmt.Fprintf(warn, "%s: legacy path: no repair role, no audit row (set %s to use the audited path)\n", tool, EnvDSN)
		}
		pool, err := db.OpenNoMigrate(ctx, legacyDSN)
		if err != nil {
			return nil, err
		}
		return &Run{Pool: pool}, nil
	}
	if !mutate {
		pool, err := db.OpenNoMigrate(ctx, dsn)
		if err != nil {
			return nil, err
		}
		return &Run{Pool: pool}, nil
	}
	role := strings.TrimSpace(getenv(EnvRole))
	reason := strings.TrimSpace(getenv(EnvReason))
	if err := validate(role, reason); err != nil {
		return nil, err
	}
	// Open the action on a connection of its own (the role may call the begin function and nothing else of the audit table), then work on a
	// pool whose every connection starts with the action's id.
	admin, err := db.OpenNoMigrate(ctx, dsn)
	if err != nil {
		return nil, err
	}
	var id string
	if err := admin.QueryRowContext(ctx, `SELECT rivolt_operator_begin($1, $2, $3, $4)::text`, role, tool, gitSHA(), reason).Scan(&id); err != nil {
		_ = admin.Close()
		return nil, fmt.Errorf("open operator action: %w", err)
	}
	pool, err := db.OpenNoMigrateWith(ctx, dsn, map[string]string{"rivolt.operator_action": id})
	if err != nil {
		_, _ = admin.ExecContext(ctx, `SELECT rivolt_operator_end($1::uuid, 'failed')`, id)
		_ = admin.Close()
		return nil, err
	}
	fmt.Fprintf(warn, "%s: operator action %s (%s) opened\n", tool, id, role)
	return &Run{Pool: pool, done: func(runErr error) error {
		outcome := "ok"
		if runErr != nil {
			outcome = "failed"
		}
		defer admin.Close()
		if _, err := admin.ExecContext(ctx, `SELECT rivolt_operator_end($1::uuid, $2)`, id, outcome); err != nil {
			return fmt.Errorf("close operator action %s: %w", id, err)
		}
		return nil
	}}, nil
}

// validate checks the label and the reason before anything is opened. The label is a role, not a person.
func validate(role, reason string) error {
	if role != "lead" && role != "owner" {
		return fmt.Errorf("%s must be lead or owner (a role, never a name), got %q", EnvRole, role)
	}
	if reason == "" {
		return fmt.Errorf("%s is required: say why this run is made", EnvReason)
	}
	if len(reason) > 300 {
		return errors.New(EnvReason + " is longer than 300 characters")
	}
	return nil
}

// gitSHA is the binary's revision, from the build info (empty for a build without it).
func gitSHA() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 40 {
				return s.Value[:40]
			}
			return s.Value
		}
	}
	return ""
}
