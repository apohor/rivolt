package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/apohor/rivolt/internal/repair/operator"
)

// currentRepairRun is the audited run of this process, if any (one repair CLI runs per process).
var currentRepairRun *operator.Run

// openRepair opens the pool a repair CLI works on: today's DATABASE_URL path, or, with RIVOLT_REPAIR_DATABASE_URL, the repair role with an
// audit row when mutating (see internal/repair/operator). mutate is the CLI's --apply; digest is its --expect-sha (the approved dry run), or empty
// to take RIVOLT_APPROVED_DIGEST.
func openRepair(ctx context.Context, tool string, mutate bool, digest string) (*sql.DB, error) {
	run, err := operator.Open(ctx, tool, mutate, digest, os.Getenv, postgresDSN(), os.Stderr)
	if err != nil {
		return nil, err
	}
	currentRepairRun = run
	return run.Pool, nil
}

// exitRepair ends a repair CLI: it closes the audit row with the exit code's outcome, then exits.
func exitRepair(code int) {
	if currentRepairRun != nil {
		var runErr error
		if code != 0 {
			runErr = fmt.Errorf("exit %d", code)
		}
		if err := currentRepairRun.Done(runErr); err != nil {
			fmt.Fprintln(os.Stderr, "repair:", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}
