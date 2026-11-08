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
	"github.com/apohor/rivolt/internal/repair/stretched"
)

// runRepairStretched caps ended_at on charge rows the stale-charge janitor stretched past their real end (see
// internal/repair/stretched, #1630). Dry run unless --apply. The dry run prints counts only (total, per vehicle id,
// skipped and flagged by reason, hours removed, rows a re-price would touch) and the sha256 of the ids with their new
// ends; --apply needs the approved dry run's --expect-count and --expect-sha, aborts unless both match, runs in one
// transaction after a CSV backup, and refuses to run while a truck is charging. Undo with --restore BATCH.
func runRepairStretched(args []string) int {
	fs := flag.NewFlagSet("repair-charge-stretched", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "change the rows (default: dry run)")
	expectCount := fs.Int("expect-count", -1, "with --apply: the approved dry run's row count (required)")
	expectSHA := fs.String("expect-sha", "", "with --apply: the approved dry run's sha256 (required)")
	backup := fs.String("backup", "", "with --apply: CSV backup path (required; must not exist)")
	idsOut := fs.String("ids-out", "", "dry run: write the sorted capped id list here (0600), for `rivolt reprice --ids`")
	flaggedOut := fs.String("flagged-out", "", "dry run: write the flagged id list here (0600) for review")
	split := fs.Bool("split", false, "split rows that hold absorbed charges (parked segments after a drive) instead of only flagging them")
	capSoC := fs.Bool("cap-by-soc", false, "also cap rows with no plugged frame and no charging after their first drive at the last pre-drive SoC rise (vehicles that report no charger status)")
	allowOpen := fs.Bool("allow-open", false, "with --apply: run although a live charge was written in the last 15 minutes")
	restore := fs.String("restore", "", "undo a batch id from charges_capped_backup (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema. The backup table comes with the server's migration.
	if err := db.RequireTables(ctx, pool, "charges", "drives", "vehicle_state", "charge_restamps", "rivian_receipts", "charges_capped_backup"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched:", err)
		return 1
	}

	if *restore != "" {
		bid, err := uuid.Parse(*restore)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-stretched: --restore needs a batch uuid")
			return 2
		}
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges_capped_backup WHERE batch_id = $1 AND restored_at IS NULL`, bid).Scan(&n); err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-stretched:", err)
			return 1
		}
		fmt.Printf("batch %s: %d backed-up row(s) to restore\n", bid, n)
		blockers, err := stretched.RestoreBlockers(ctx, pool, bid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-stretched:", err)
			return 1
		}
		for _, b := range blockers {
			fmt.Printf("  blocked: row %s: %s\n", b.ChargeID.String()[:8], b.Reason)
		}
		if len(blockers) > 0 {
			fmt.Println("the restore is all or nothing: nothing can be restored while a group is blocked")
		}
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		restored, err := stretched.Restore(ctx, pool, bid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-stretched: restore:", err)
			return 1
		}
		fmt.Printf("restored %d row(s)\n", restored)
		return 0
	}

	if *apply && (*expectCount < 0 || *expectSHA == "") {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: --apply requires --expect-count and --expect-sha from the approved dry run")
		return 2
	}
	if *apply && *backup == "" {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: --apply requires --backup <csv path>")
		return 2
	}
	opts := stretched.Options{Split: *split, CapBySoC: *capSoC}
	if *split {
		opts.Price = stretched.NewPriceFn(ctx, pool)
	}
	rows, err := stretched.Select(ctx, pool, false, *split || *capSoC)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: select:", err)
		return 1
	}
	plan := stretched.MakePlan(rows, time.Now(), opts)
	ids := plan.IDs()
	open, err := blips.OpenCharges(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched:", err)
		return 1
	}

	fmt.Printf("candidates (live, a drive inside): %d\n", len(rows))
	fmt.Printf("to cap: %d\n", len(ids))
	byVehicle := plan.ByVehicle()
	fmt.Printf("vehicles: %d\n", len(byVehicle))
	vids := make([]uuid.UUID, 0, len(byVehicle))
	for v := range byVehicle {
		vids = append(vids, v)
	}
	sort.Slice(vids, func(i, j int) bool {
		return byVehicle[vids[i]] > byVehicle[vids[j]] || (byVehicle[vids[i]] == byVehicle[vids[j]] && vids[i].String() < vids[j].String())
	})
	for _, v := range vids {
		fmt.Printf("  vehicle %s: %d\n", v.String()[:8], byVehicle[v])
	}
	fmt.Printf("hours removed: %.1f\n", plan.HoursRemoved())
	if *capSoC {
		n, off := plan.SoCModeTotals()
		fmt.Printf("capped by SoC rise (mode A, no plugged frame and no charging after the first drive): %d row(s), %d of them with stored kWh disagreeing with the pre-drive SoC gain by more than 25%% (capped; energy not changed)\n", n, off)
	}
	if *split {
		n, created, kwh := plan.SplitTotals()
		acHome, acAway, acOther, dc := plan.NewRowTypes()
		fmt.Printf("new rows by type: %d AC at home, %d AC away, %d AC with no place, %d DC fast (DC fast rows are left unpriced here: `rivolt reprice --detect` prices them)\n", acHome, acAway, acOther, dc)
		fmt.Printf("split originals that are empty (no charge before the first drive; kept as zero-energy abandoned rows ended at the first drive): %d\n", plan.EmptyOriginals())
		cr, ck := plan.ClampTotals()
		fmt.Printf("clamped (new rows scaled down to the stored energy, excess within max(1 kWh, 5%% of stored)): %d row(s), %.1f kWh\n", cr, ck)
		origDelta, newCost := plan.CostDelta()
		fmt.Printf("cost delta: originals %+.2f, new rows %+.2f, net %+.2f\n", origDelta, newCost, origDelta+newCost)
		byV := plan.SplitsByVehicle()
		vs := make([]uuid.UUID, 0, len(byV))
		for v := range byV {
			vs = append(vs, v)
		}
		sort.Slice(vs, func(i, j int) bool {
			return byV[vs[i]] > byV[vs[j]] || (byV[vs[i]] == byV[vs[j]] && vs[i].String() < vs[j].String())
		})
		for _, v := range vs {
			fmt.Printf("  splits on vehicle %s: %d\n", v.String()[:8], byV[v])
		}
		bySrc := plan.PricedBySource()
		srcs := make([]string, 0, len(bySrc))
		for k := range bySrc {
			srcs = append(srcs, k)
		}
		sort.Strings(srcs)
		for _, k := range srcs {
			fmt.Printf("  new rows by cost source %s: %d\n", k, bySrc[k])
		}
		fmt.Printf("split: %d row(s) create %d new row(s), %.1f kWh re-attributed, %.1f h of idle inside the new rows (AC rows are priced at the home rate where the vehicle was at home, and placed; the rest stay unpriced)\n", n, created, kwh, plan.IdleHoursInNewRows())
	}
	fmt.Printf("capped rows priced from the home rate or a network estimate (re-price candidates): %d\n", plan.Repriceable())
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
	fmt.Printf("sha256 of the capped ids and their new ends: %s\n", digest)
	writeIDs := func(path string, list []uuid.UUID) bool {
		if path == "" {
			return true
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-stretched: ids out:", err)
			return false
		}
		for _, id := range list {
			fmt.Fprintln(f, id)
		}
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-stretched: ids out:", err)
			return false
		}
		fmt.Printf("ids written to %s\n", path)
		return true
	}
	if !writeIDs(*idsOut, ids) || !writeIDs(*flaggedOut, plan.FlaggedIDs()) {
		return 1
	}
	if !*apply {
		fmt.Println("dry run; nothing written. Apply with --apply --expect-count N --expect-sha SHA --backup CSV.")
		return 0
	}

	batch := uuid.New()
	bk, err := newPhantomCSV(*backup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: backup:", err)
		return 1
	}
	defer func() { _ = bk.Close() }() // error paths; the end checks the close below
	fmt.Printf("backup file: %s (batch %s)\n", *backup, batch)
	n, err := stretched.Apply(ctx, pool, batch, *expectCount, *expectSHA, "stretched charge row: ended_at capped at the last plugged frame", *allowOpen, opts, bk.sink(batch))
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: NOT applied:", err)
		return 1
	}
	if err := bk.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-stretched: backup close:", err)
		return 1
	}
	fmt.Printf("capped %d charge(s) in one transaction (batch %s)\n", n, batch)
	return 0
}
