package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/blips"
	"github.com/apohor/rivolt/internal/repair/phantom"
)

// runRepairBlips deletes the wake-blip charges (see internal/repair/blips): live charges under
// 20 s that moved the battery under half a point. Dry run unless --apply. The dry run prints
// counts only (total, per vehicle id, per month, skipped by reason) and the sha256 of the sorted
// id list; --apply needs the approved dry run's --expect-count and --expect-sha and aborts unless
// both match, in one transaction, after a CSV backup, and refuses to run while a truck is
// charging. Undo with --restore BATCH or --restore-csv FILE (the phantom repair's restore).
func runRepairBlips(args []string) int {
	fs := flag.NewFlagSet("repair-charge-blips", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "delete the rows (default: dry run)")
	expectCount := fs.Int("expect-count", -1, "with --apply: the approved dry run's row count (required)")
	expectSHA := fs.String("expect-sha", "", "with --apply: the approved dry run's sha256 of the sorted id list (required)")
	backup := fs.String("backup", "", "with --apply: CSV backup path (required; must not exist)")
	idsOut := fs.String("ids-out", "", "dry run: write the sorted id list here (0600) for the audit")
	allowOpen := fs.Bool("allow-open", false, "with --apply: run although a live charge was written in the last 15 minutes")
	restore := fs.String("restore", "", "undo a batch id from charges_deleted_backup (dry run unless --apply)")
	restoreCSV := fs.String("restore-csv", "", "undo from a --backup CSV (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema.
	if err := db.RequireTables(ctx, pool, "charges", "vehicles", "vehicle_pack_health_samples", "charge_restamps", "rivian_receipts"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips:", err)
		return 1
	}
	hasBackupTable, err := phantom.BackupTableExists(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips:", err)
		return 1
	}

	if *restoreCSV != "" {
		rows, err := readPhantomCSV(*restoreCSV)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips:", err)
			return 1
		}
		fmt.Printf("%s: %d row(s) to restore\n", *restoreCSV, len(rows))
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		res, err := phantom.RestoreRows(ctx, pool, rows)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips: restore:", err)
			return 1
		}
		fmt.Printf("restored %d charge(s), %d pack-health sample(s)\n", res.Charges, res.PackHealth)
		return 0
	}
	if *restore != "" {
		if !hasBackupTable {
			fmt.Fprintln(os.Stderr, "repair-charge-blips: --restore needs the backup table; use --restore-csv")
			return 2
		}
		bid, err := uuid.Parse(*restore)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips: --restore needs a batch uuid")
			return 2
		}
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges_deleted_backup WHERE batch_id = $1 AND restored_at IS NULL`, bid).Scan(&n); err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips:", err)
			return 1
		}
		fmt.Printf("batch %s: %d backed-up row(s) to restore\n", bid, n)
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		res, err := phantom.Restore(ctx, pool, bid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips: restore:", err)
			return 1
		}
		fmt.Printf("restored %d charge(s), %d pack-health sample(s)\n", res.Charges, res.PackHealth)
		return 0
	}

	if *apply && (*expectCount < 0 || *expectSHA == "") {
		fmt.Fprintln(os.Stderr, "repair-charge-blips: --apply requires --expect-count and --expect-sha from the approved dry run")
		return 2
	}
	if *apply && *backup == "" {
		fmt.Fprintln(os.Stderr, "repair-charge-blips: --apply requires --backup <csv path>")
		return 2
	}
	rows, err := blips.Select(ctx, pool, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips: select:", err)
		return 1
	}
	plan := blips.MakePlan(rows)
	ids := plan.IDs()
	digest := blips.Digest(ids)
	open, err := blips.OpenCharges(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips:", err)
		return 1
	}

	fmt.Printf("candidates (live, under 20 s): %d\n", len(rows))
	fmt.Printf("to delete: %d\n", len(ids))
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
	months := plan.ByMonth()
	ms := make([]string, 0, len(months))
	for m := range months {
		ms = append(ms, m)
	}
	sort.Strings(ms)
	for _, m := range ms {
		fmt.Printf("  %s: %d\n", m, months[m])
	}
	packs := 0
	for _, r := range plan.Delete {
		if r.HasPack {
			packs++
		}
	}
	fmt.Printf("with a pack-health sample (cascades, backed up): %d\n", packs)
	reasons := make([]string, 0, len(plan.Skipped))
	for r := range plan.Skipped {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Printf("skipped (%s): %d\n", r, plan.Skipped[r])
	}
	fmt.Printf("open live charges written in the last 15 minutes: %d\n", open)
	fmt.Printf("sha256 of the sorted id list: %s\n", digest)
	if hasBackupTable {
		fmt.Println("backup table charges_deleted_backup present: backing up to it and the CSV")
	} else {
		fmt.Println("backup table charges_deleted_backup absent: the CSV is the only backup; undo with --restore-csv")
	}
	if *idsOut != "" {
		f, err := os.OpenFile(*idsOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips: ids-out:", err)
			return 1
		}
		for _, id := range ids {
			fmt.Fprintln(f, id)
		}
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-blips: ids-out:", err)
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
		fmt.Fprintln(os.Stderr, "repair-charge-blips: backup:", err)
		return 1
	}
	defer func() { _ = bk.Close() }() // error paths; the end checks the close below
	fmt.Printf("backup file: %s (batch %s)\n", *backup, batch)
	n, err := blips.Apply(ctx, pool, batch, *expectCount, *expectSHA, "wake blip charge (under 20 s, SoC change under 0.5)", *allowOpen, bk.sink(batch))
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips: NOT applied:", err)
		return 1
	}
	if err := bk.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-blips: backup close:", err)
		return 1
	}
	fmt.Printf("deleted %d charge(s) in one transaction (batch %s)\n", n, batch)
	return 0
}
