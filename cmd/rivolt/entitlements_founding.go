package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/entitlements"
)

// runEntitlementsFounding grants the founding year: Pro free for one year from launch to every account that exists at launch, the demo
// account excluded (entitlement source founding, no card, nothing auto-charges).
//
// Dry run by default: prints the counts, the sha256 of the sorted ids to grant, and user id prefixes only, never emails. --apply needs
// --expect-count and --expect-sha from that dry run, so the run that applies is the run that was reviewed; it grants in one transaction,
// checks that founding rows = accounts at launch minus the demo, and rolls back if not. Idempotent: a re-run grants nothing. Never
// migrates the database.
func runEntitlementsFounding(args []string) int {
	fs := flag.NewFlagSet("entitlements-founding", flag.ContinueOnError)
	launchFlag := fs.String("launch", "", "launch instant, RFC3339 (required): accounts created at or before it are granted; the year runs from it")
	apply := fs.Bool("apply", false, "grant (default: dry run)")
	expectCount := fs.Int("expect-count", -1, "with --apply: the number to grant, from the dry run")
	expectSHA := fs.String("expect-sha", "", "with --apply: the ids hash from the dry run")
	redateFrom := fs.String("redate-from", "", "re-date mode: RFC3339 end the founding rows carry now; they move to the end for --launch, and accounts without a row are granted")
	expectGrantCount := fs.Int("expect-grant-count", -1, "re-date with --apply: the number to grant, from the dry run (--expect-count is the number to move)")
	expectGrantSHA := fs.String("expect-grant-sha", "", "re-date with --apply: the grant ids hash from the dry run (--expect-sha is the move hash)")
	undoFile := fs.String("undo-file", "", "re-date with --apply: a new file the undo SQL is written to before anything changes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	launch, err := time.Parse(time.RFC3339, *launchFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "entitlements-founding: --launch must be an RFC3339 time")
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "entitlements-founding: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "users", "entitlements"); err != nil {
		fmt.Fprintln(os.Stderr, "entitlements-founding:", err)
		return 1
	}
	if *redateFrom != "" {
		from, err := time.Parse(time.RFC3339, *redateFrom)
		if err != nil {
			fmt.Fprintln(os.Stderr, "entitlements-founding: --redate-from must be an RFC3339 time")
			return 2
		}
		//rivolt:transient a subcommand: runs once, in the foreground, and exits
		return entitlements.RunRedate(ctx, pool, os.Stdout, os.Stderr, demo.UserID(), launch, from, *apply,
			entitlements.RedateExpect{MoveCount: *expectCount, MoveSHA: *expectSHA, GrantCount: *expectGrantCount, GrantSHA: *expectGrantSHA}, *undoFile)
	}
	//rivolt:transient a subcommand: runs once, in the foreground, and exits
	return entitlements.RunFounding(ctx, pool, os.Stdout, os.Stderr, demo.UserID(), launch, *apply, *expectCount, *expectSHA)
}
