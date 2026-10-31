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
	return entitlements.RunFounding(ctx, pool, os.Stdout, os.Stderr, demo.UserID(), launch, *apply, *expectCount, *expectSHA)
}
