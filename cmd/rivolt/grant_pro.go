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

// runGrantPro gives one account Pro at no charge until a date (entitlement source grant), or ends such a grant early with --revoke. The
// operator tool for the few accounts the owner comps by hand: one account per run, found by its sign-in email, an end date at most 400 days
// ahead, refused when the account already has Pro that ends later, written in one transaction with its admin audit row. --dry-run prints
// the plan and writes nothing. Never migrates the database.
func runGrantPro(args []string) int {
	fs := flag.NewFlagSet("grant-pro", flag.ContinueOnError)
	emailFlag := fs.String("email", "", "the account's sign-in email (required; exactly one account must match)")
	until := fs.String("until", "", "last day of Pro, YYYY-MM-DD (required unless --revoke; after today, at most 400 days ahead)")
	reason := fs.String("reason", "", "why, one line without an email address (required; stored in the audit log)")
	dry := fs.Bool("dry-run", false, "print what would happen and write nothing")
	revoke := fs.Bool("revoke", false, "end the account's grant early (only a row with source grant)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "grant-pro: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	// Check the flags before the database is opened, so a typo costs no connection.
	p := entitlements.GrantParams{Email: *emailFlag, Until: *until, Reason: *reason, Revoke: *revoke, DryRun: *dry}
	if code := entitlements.CheckGrantFlags(p, time.Now()); code != 0 {
		return code
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "grant-pro: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "users", "entitlements", "admin_audit"); err != nil {
		fmt.Fprintln(os.Stderr, "grant-pro:", err)
		return 1
	}
	//rivolt:transient a subcommand: runs once, in the foreground, and exits
	return entitlements.RunGrantPro(ctx, pool, os.Stdout, os.Stderr, demo.UserID(), time.Now(), p)
}
