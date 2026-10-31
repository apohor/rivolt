package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/preflight"
)

// runLaunchPreflight prints the read-only report an operator reads before the Rivolt Pro flip (docs/LAUNCH_FLIP.md): the launch-readiness
// list, the founding grant's dry-run counts, whether the Terms and notice pages answer, and which billing settings are present (names and
// state only, never a value). It reads the process environment, so run it where the server's settings are (the app pod). It writes nothing
// and switches nothing on; exit 0 when nothing stops the flip, 1 otherwise, 2 for usage.
func runLaunchPreflight(args []string) int {
	fs := flag.NewFlagSet("launch-preflight", flag.ContinueOnError)
	launchFlag := fs.String("launch", "", "launch instant, RFC3339 (default: now): the founding grant counts accounts created at or before it")
	offline := fs.Bool("offline", false, "skip the URL checks (no network)")
	noDB := fs.Bool("no-db", false, "skip the founding dry run (no database)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	launch := time.Now().UTC()
	if *launchFlag != "" {
		t, err := time.Parse(time.RFC3339, *launchFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "launch-preflight: --launch must be an RFC3339 time")
			return 2
		}
		launch = t
	}
	in := preflight.Input{Getenv: os.Getenv, Launch: launch}
	ctx := context.Background()
	if !*noDB {
		pool, err := db.OpenNoMigrate(ctx, postgresDSN())
		if err != nil {
			fmt.Fprintln(os.Stderr, "launch-preflight: postgres:", err)
			return 1
		}
		defer pool.Close()
		if err := db.RequireTables(ctx, pool, "users", "entitlements"); err != nil {
			fmt.Fprintln(os.Stderr, "launch-preflight:", err)
			return 1
		}
		in.Founding = preflight.FoundingFromDB(pool, demo.UserID())
	}
	if !*offline {
		in.Fetch = preflight.HTTPFetch
	}
	rep := preflight.Run(ctx, in)
	rep.Print(os.Stdout)
	if !rep.Ready() {
		return 1
	}
	return 0
}
