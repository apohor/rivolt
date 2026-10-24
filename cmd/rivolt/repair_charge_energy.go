package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/chargeenergy"
)

// runRepairChargeEnergy is `rivolt repair-charge-energy`: repairs the energy (and so the cost) of AC home charges the
// recorder under-recorded because every pod handover mid-charge restarted their energy integral at zero. See
// internal/repair/chargeenergy. Dry run by default; it never migrates; a write needs an audited id list, one user, and a
// fresh backup file.
func runRepairChargeEnergy(args []string) int {
	fs := flag.NewFlagSet("repair-charge-energy", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the repair (default: dry run)")
	sinceFlag := fs.String("since", "2026-10-15", "repair charges that started on or after this date (UTC)")
	idsOut := fs.String("ids-out", "", "dry run: write the ids it would repair to this file (must not exist)")
	idsPath := fs.String("ids", "", "with --apply: the audited id file (one charge id per line)")
	user := fs.String("user", "", "with --apply: the username to repair (approval is per user)")
	backup := fs.String("backup", "", "with --apply: backup file (JSON lines, must not exist)")
	restore := fs.String("restore-json", "", "undo from a --backup file (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	since, err := time.Parse("2006-01-02", *sinceFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-energy: --since must be YYYY-MM-DD")
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-energy: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema.
	if err := db.RequireTables(ctx, pool, "charges", "vehicles", "users"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-energy:", err)
		return 1
	}

	if *restore != "" {
		rows, err := chargeenergy.ReadBackup(*restore)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-energy:", err)
			return 1
		}
		fmt.Printf("%s: %d row(s) to restore\n", *restore, len(rows))
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		n, err := chargeenergy.Restore(ctx, pool, rows)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-charge-energy: restore:", err)
			return 1
		}
		fmt.Printf("restored %d of %d row(s) (a row edited since the repair is left as it is)\n", n, len(rows))
		return 0
	}

	rows, clean, err := chargeenergy.Load(ctx, pool, since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-energy:", err)
		return 1
	}
	plan := chargeenergy.Plan(rows, clean)

	if !*apply {
		fmt.Printf("dry run: AC live charges since %s with at least %.0f %% SoC gain; a row is repaired when its energy per SoC %% is under %.0f %% of its vehicle's estimate\n",
			*sinceFlag, chargeenergy.MinSoC, chargeenergy.LowRatio*100)
		var ids []string
		for _, p := range plan {
			if p.Verdict.Repair {
				ids = append(ids, p.Row.ID.String())
			}
		}
		for _, s := range chargeenergy.Summarise(plan) {
			fmt.Printf("  %-4s considered %3d  to repair %3d  accounts %2d  (own median %d, pack x factor %d)  kWh %.0f -> %.0f\n",
				s.Model, s.Considered, s.Repair, s.Accounts, s.OwnMedian, s.PackBased, s.KWhBefore, s.KWhAfter)
		}
		fmt.Printf("%d row(s) in all would be repaired\n", len(ids))
		if *idsOut != "" {
			f, err := os.OpenFile(*idsOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				fmt.Fprintln(os.Stderr, "repair-charge-energy: --ids-out:", err)
				return 1
			}
			_, werr := f.WriteString(strings.Join(ids, "\n") + "\n")
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				fmt.Fprintln(os.Stderr, "repair-charge-energy: --ids-out:", werr)
				return 1
			}
			fmt.Println("ids written to", *idsOut)
		}
		fmt.Println("dry run; nothing was written")
		return 0
	}

	if *idsPath == "" || *user == "" || *backup == "" {
		fmt.Fprintln(os.Stderr, "repair-charge-energy: --apply needs --ids (the audited list), --user and --backup")
		return 2
	}
	ids, err := parseIDFile(*idsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-energy:", err)
		return 1
	}
	var uid uuid.UUID
	if err := pool.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(username) = lower($1)`, *user).Scan(&uid); err != nil {
		fmt.Fprintf(os.Stderr, "repair-charge-energy: --user: no such user (%v)\n", err)
		return 2
	}
	changed, skipped, err := chargeenergy.Apply(ctx, pool, uid, ids, since, *backup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-energy:", err)
		return 1
	}
	fmt.Printf("repaired %d row(s); skipped %d (other users' ids, or the rule no longer held); backup: %s\n", changed, skipped, *backup)
	return 0
}
