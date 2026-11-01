package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/routegaps"
	"github.com/apohor/rivolt/internal/repair/routepoints"
)

// runRepairRoutePoints audits, and repairs, stored drive routes that hold a point which is not on the globe (see internal/repair/routepoints).
// The dry run IS the audit: it reads every closed drive's stored route and prints counts only (routes read, routes that could not be read,
// routes holding a bad point, bad points, routes that would have nothing left, the work list per month) and the sha256 of the sorted id list;
// never a position or an id. --apply needs the approved dry run's --expect-count and --expect-sha (it aborts unless both match) and
// --backup (a new 0600 CSV of every replaced route, written before each update); an update only applies if the stored route is still the one
// the audit read. The repair drops the bad points and keeps the rest of the route exactly as it was. Undo with --restore-csv FILE [--apply].
// Never migrates (OpenNoMigrate): run it only from a released binary.
func runRepairRoutePoints(args []string) int {
	fs := flag.NewFlagSet("repair-route-points", flag.ContinueOnError)
	sinceS := fs.String("since", "", "first drive start, RFC3339 or YYYY-MM-DD (default: all of them)")
	untilS := fs.String("until", "", "drive starts before this, RFC3339 or YYYY-MM-DD (default: now)")
	apply := fs.Bool("apply", false, "rewrite the routes (default: dry run, the audit)")
	expectCount := fs.Int("expect-count", -1, "with --apply: the approved dry run's drive count (required)")
	expectSHA := fs.String("expect-sha", "", "with --apply: the approved dry run's sha256 of the sorted id list (required)")
	backup := fs.String("backup", "", "with --apply: a new CSV for the backup (required; must not exist)")
	restoreCSV := fs.String("restore-csv", "", "undo an apply from its --backup CSV (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *restoreCSV == "" && *apply {
		switch {
		case *expectCount < 0 || *expectSHA == "":
			fmt.Fprintln(os.Stderr, "repair-route-points: --apply requires --expect-count and --expect-sha from the approved dry run")
			return 2
		case *backup == "":
			fmt.Fprintln(os.Stderr, "repair-route-points: --apply requires --backup <csv path>")
			return 2
		}
	}
	now := time.Now().UTC()
	since, err := parseRepairTime(*sinceS, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: --since:", err)
		return 2
	}
	until, err := parseRepairTime(*untilS, now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: --until:", err)
		return 2
	}

	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema.
	if err := db.RequireTables(ctx, pool, "drives"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points:", err)
		return 1
	}

	if *restoreCSV != "" {
		rows, err := readRouteGapsCSV(*restoreCSV) // the same backup format as repair-route-gaps
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-route-points:", err)
			return 1
		}
		fmt.Printf("%s: %d route(s) to restore\n", *restoreCSV, len(rows))
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		res, err := routegaps.Restore(ctx, pool, rows)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-route-points: restore:", err)
			return 1
		}
		fmt.Printf("restored %d route(s); skipped %d (changed since the repair)\n", res.Restored, res.Skipped)
		return 0
	}

	cands, err := routepoints.Select(ctx, pool, since, until)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: select:", err)
		return 1
	}
	plan := routepoints.MakePlan(cands)
	ids := plan.IDs()
	digest := routepoints.Digest(ids)
	fmt.Printf("window: %s to %s\n", since.Format("2006-01-02"), until.Format("2006-01-02"))
	fmt.Printf("closed drives with a stored route: %d\n", plan.Scanned)
	fmt.Printf("routes that could not be read: %d\n", plan.Undecoded)
	fmt.Printf("routes holding a point off the globe, repairable: %d\n", len(ids))
	fmt.Printf("routes holding one with fewer than two points left (left alone): %d\n", plan.Unfixable)
	fmt.Printf("points off the globe: %d\n", plan.BadPoints)
	months := plan.ByMonth()
	ms := make([]string, 0, len(months))
	for m := range months {
		ms = append(ms, m)
	}
	sort.Strings(ms)
	for _, m := range ms {
		fmt.Printf("  %s: %d\n", m, months[m])
	}
	fmt.Printf("sha256 of the sorted id list: %s\n", digest)
	if !*apply {
		fmt.Println("dry run; nothing written. Apply with --apply --expect-count N --expect-sha SHA --backup CSV.")
		return 0
	}
	if len(ids) != *expectCount || digest != *expectSHA {
		fmt.Fprintf(os.Stderr, "repair-route-points: NOT applied: the plan is %d drives / %s, the approved dry run said %d / %s\n", len(ids), digest, *expectCount, *expectSHA)
		return 1
	}

	bk, err := newRouteGapsCSV(*backup) // 0600, exclusive create, synced per row
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: backup:", err)
		return 1
	}
	defer func() { _ = bk.f.Close() }() // error paths; the end checks the close below
	fmt.Printf("backup file: %s\n", *backup)
	res, err := routepoints.Apply(ctx, pool, plan, bk.write)
	fmt.Printf("rewritten %d route(s), %d point(s) dropped; %d changed meanwhile and left alone\n", res.Updated, res.Removed, res.Raced)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: stopped:", err)
		return 1
	}
	if err := bk.f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-points: backup close:", err)
		return 1
	}
	return 0
}
