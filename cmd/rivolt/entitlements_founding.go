package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

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
	return foundingRun(ctx, pool, os.Stdout, os.Stderr, demo.UserID(), launch, *apply, *expectCount, *expectSHA)
}

// foundingRun is the CLI's body (a function so a test drives it against Postgres).
func foundingRun(ctx context.Context, pool *sql.DB, out, errOut io.Writer, demoID uuid.UUID, launch time.Time, apply bool, expectCount int, expectSHA string) int {
	rep, err := entitlements.PlanFounding(ctx, pool, demoID, launch)
	if err != nil {
		fmt.Fprintln(errOut, "entitlements-founding:", err)
		return 1
	}
	fmt.Fprintf(out, "launch %s, founding year ends %s\n", launch.UTC().Format(time.RFC3339), entitlements.FoundingEnd(launch).Format(time.RFC3339))
	fmt.Fprintf(out, "accounts at launch (demo excluded): %d, already founding: %d, to grant: %d\n", rep.Eligible, rep.Already, rep.ToGrant)
	fmt.Fprintf(out, "ids sha256: %s\n", rep.IDsHash())
	if !apply {
		for _, id := range rep.IDs {
			fmt.Fprintf(out, "  %s\n", id.String()[:8])
		}
		fmt.Fprintln(out, "dry run: nothing written (--apply --expect-count N --expect-sha SHA to grant)")
		return 0
	}
	if expectCount != rep.ToGrant || expectSHA != rep.IDsHash() {
		fmt.Fprintf(errOut, "entitlements-founding: the accounts changed since the dry run (expected %d / %s, now %d / %s); run the dry run again\n",
			expectCount, expectSHA, rep.ToGrant, rep.IDsHash())
		return 1
	}
	n, err := entitlements.ApplyFounding(ctx, pool, demoID, launch)
	if err != nil {
		fmt.Fprintln(errOut, "entitlements-founding:", err)
		return 1
	}
	fmt.Fprintf(out, "granted %d; founding rows now equal the accounts at launch minus the demo\n", n)
	return 0
}
