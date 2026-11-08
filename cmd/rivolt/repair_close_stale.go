package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/blips"
	"github.com/apohor/rivolt/internal/repair/closestale"
)

// runRepairCloseStale closes open-state live charge rows the stale-charge janitor can never reach: a row left open by an account
// that went dormant (see internal/repair/closestale, #1641). final_state by the SoC rule; ended_at and the rest untouched. Dry run
// unless --apply. The dry run prints counts only (total, by new state, per vehicle id, skipped by reason) and the sha256; --apply
// needs the approved dry run's --expect-count and --expect-sha, aborts unless both match, runs in one transaction after a CSV
// backup, and refuses to run while a truck is charging. Undo with --restore BATCH (all or nothing; a dry run lists the blockers).
func runRepairCloseStale(args []string) int {
	fs := flag.NewFlagSet("repair-charge-close-stale", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "change the rows (default: dry run)")
	expectCount := fs.Int("expect-count", -1, "with --apply: the approved dry run's row count (required)")
	expectSHA := fs.String("expect-sha", "", "with --apply: the approved dry run's sha256 (required)")
	backup := fs.String("backup", "", "with --apply: CSV backup path (required; must not exist)")
	idsOut := fs.String("ids-out", "", "dry run: write the sorted id list here (0600)")
	allowOpen := fs.Bool("allow-open", false, "with --apply: run although a live charge was written in the last 15 minutes")
	restore := fs.String("restore", "", "undo a batch id from charges_capped_backup (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema. The backup table comes with the server's migration.
	if err := db.RequireTables(ctx, pool, "charges", "vehicle_state", "charge_restamps", "rivian_receipts", "charges_capped_backup"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale:", err)
		return 1
	}

	if *restore != "" {
		bid, err := uuid.Parse(*restore)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-close-stale: --restore needs a batch uuid")
			return 2
		}
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges_capped_backup WHERE batch_id = $1 AND restored_at IS NULL`, bid).Scan(&n); err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-close-stale:", err)
			return 1
		}
		fmt.Printf("batch %s: %d backed-up row(s) to restore\n", bid, n)
		blockers, err := closestale.RestoreBlockers(ctx, pool, bid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-close-stale:", err)
			return 1
		}
		for _, b := range blockers {
			fmt.Printf("  blocked: row %s: %s\n", b.ChargeID.String()[:8], b.Reason)
		}
		if len(blockers) > 0 {
			fmt.Println("the restore is all or nothing: nothing can be restored while a row is blocked")
		}
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		restored, err := closestale.Restore(ctx, pool, bid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-close-stale: restore:", err)
			return 1
		}
		fmt.Printf("restored %d row(s)\n", restored)
		return 0
	}

	if *apply && (*expectCount < 0 || *expectSHA == "") {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: --apply requires --expect-count and --expect-sha from the approved dry run")
		return 2
	}
	if *apply && *backup == "" {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: --apply requires --backup <csv path>")
		return 2
	}
	rows, err := closestale.Select(ctx, pool, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: select:", err)
		return 1
	}
	plan := closestale.MakePlan(rows, time.Now())
	ids := plan.IDs()
	open, err := blips.OpenCharges(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale:", err)
		return 1
	}
	fmt.Printf("candidates (open-state live rows last written over a day ago): %d\n", len(rows))
	fmt.Printf("to close: %d\n", len(ids))
	byState := plan.ByState()
	states := make([]string, 0, len(byState))
	for s := range byState {
		states = append(states, s)
	}
	sort.Strings(states)
	for _, s := range states {
		fmt.Printf("  becomes %s: %d\n", s, byState[s])
	}
	byV := plan.ByVehicle()
	fmt.Printf("vehicles: %d\n", len(byV))
	vs := make([]uuid.UUID, 0, len(byV))
	for v := range byV {
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool {
		return byV[vs[i]] > byV[vs[j]] || (byV[vs[i]] == byV[vs[j]] && vs[i].String() < vs[j].String())
	})
	for _, v := range vs {
		fmt.Printf("  vehicle %s: %d\n", v.String()[:8], byV[v])
	}
	reasons := make([]string, 0, len(plan.Skipped))
	for r := range plan.Skipped {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Printf("skipped (%s): %d\n", r, plan.Skipped[r])
	}
	fmt.Printf("open live charges written in the last 15 minutes: %d\n", open)
	digest := plan.Digest()
	fmt.Printf("sha256 of the ids with their new states: %s\n", digest)
	if *idsOut != "" {
		f, err := os.OpenFile(*idsOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-close-stale: ids-out:", err)
			return 1
		}
		for _, id := range ids {
			fmt.Fprintln(f, id)
		}
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-close-stale: ids-out:", err)
			return 1
		}
		fmt.Printf("ids written to %s\n", *idsOut)
	}
	if !*apply {
		fmt.Println("dry run; nothing written. Apply with --apply --expect-count N --expect-sha SHA --backup CSV.")
		return 0
	}
	batch := uuid.New()
	bk, err := newPhantomCSV(*backup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: backup:", err)
		return 1
	}
	defer func() { _ = bk.Close() }() // error paths; the end checks the close below
	fmt.Printf("backup file: %s (batch %s)\n", *backup, batch)
	n, err := closestale.Apply(ctx, pool, batch, *expectCount, *expectSHA, "stale open charge row left by a dormant account: closed by the SoC rule", *allowOpen, bk.sink(batch))
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: NOT applied:", err)
		return 1
	}
	if err := bk.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-close-stale: backup close:", err)
		return 1
	}
	fmt.Printf("closed %d charge(s) in one transaction (batch %s)\n", n, batch)
	return 0
}
