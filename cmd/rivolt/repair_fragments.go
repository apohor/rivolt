package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair"
)

// runRepairChargeFragments is `rivolt repair-charge-fragments`: merge
// the back-to-back pieces of one charging session that the recorder
// stored as several rows, each carrying the running energy total
// (internal/repair). Dry run unless --apply; --apply first writes a CSV
// backup of every touched row and refuses to continue without it.
func runRepairChargeFragments(args []string) int {
	fs := flag.NewFlagSet("repair-charge-fragments", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the changes (default: dry run)")
	user := fs.String("user", "", "only this username")
	maxGap := fs.Duration("max-gap", 200*time.Second, "largest gap between one piece's end and the next piece's start")
	sinceS := fs.String("since", "", "only pieces starting at/after this RFC 3339 time")
	untilS := fs.String("until", "", "only pieces starting before this RFC 3339 time")
	backup := fs.String("backup", "", "CSV backup path (default ./charge-fragments-backup-<UTC>.csv); required to write with --apply")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	parse := func(s string) (time.Time, bool) {
		if s == "" {
			return time.Time{}, true
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "repair-charge-fragments: bad time %q (want RFC 3339)\n", s)
			return time.Time{}, false
		}
		return t, true
	}
	since, ok1 := parse(*sinceS)
	until, ok2 := parse(*untilS)
	if !ok1 || !ok2 {
		return 2
	}
	if *maxGap <= 0 || *maxGap > 10*time.Minute {
		fmt.Fprintln(os.Stderr, "repair-charge-fragments: --max-gap must be in (0, 10m]")
		return 2
	}

	ctx := context.Background()
	// Never migrate: a binary newer than the database must not change
	// the production schema as a side effect of a repair.
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-fragments: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "charges", "vehicles", "vehicle_state", "charge_restamps"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-fragments:", err)
		return 1
	}

	pieces, err := repair.Load(ctx, pool, *user, since, until, *maxGap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-fragments: load:", err)
		return 1
	}
	groups, skipped := repair.FindGroups(pieces, *maxGap)
	for _, sk := range skipped {
		k := sk.Group.Pieces[0]
		fmt.Printf("SKIPPED %s vehicle=%s %s: %s\n", k.Username, k.RivianID, k.StartedAt.Format(time.RFC3339), sk.Reason)
	}
	if len(groups) == 0 {
		fmt.Println("repair-charge-fragments: no fragmented sessions found")
		return 0
	}

	var countedKWh, countedCost, finalKWh, finalCost float64
	removed := 0
	for _, g := range groups {
		m := g.Plan()
		k := g.Keeper()
		fmt.Printf("%s vehicle=%s %s .. %s  %d pieces -> 1 (keeps %s)  counted %.1f kWh / $%.2f  becomes %.1f kWh / $%.2f\n",
			k.Username, k.RivianID, m.StartedAt.Format(time.RFC3339), m.EndedAt.Format(time.RFC3339),
			len(g.Pieces), k.ExternalID, m.CountedKWh, m.CountedCost, valOr0(m.EnergyKWh), valOr0(m.Cost))
		countedKWh += m.CountedKWh
		countedCost += m.CountedCost
		finalKWh += valOr0(m.EnergyKWh)
		finalCost += valOr0(m.Cost)
		removed += len(g.Removed())
	}
	fmt.Printf("%d sessions, %d rows to delete; energy %.1f -> %.1f kWh, cost %.2f -> %.2f\n",
		len(groups), removed, countedKWh, finalKWh, countedCost, finalCost)
	if !*apply {
		fmt.Println("dry run: nothing written (re-run with --apply, --user, --since, --until and a --backup path)")
		return 0
	}
	// A fleet-wide heuristic must never write: apply only to a window
	// someone has already looked at in a dry run.
	if *user == "" || since.IsZero() || until.IsZero() {
		fmt.Fprintln(os.Stderr, "repair-charge-fragments: --apply needs --user, --since and --until (a verified window)")
		return 2
	}

	path := *backup
	if path == "" {
		path = "charge-fragments-backup-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	}
	if err := writeFragmentsBackup(path, groups); err != nil {
		fmt.Fprintln(os.Stderr, "repair-charge-fragments: refusing to apply, backup failed:", err)
		return 1
	}
	fmt.Println("backup written:", path)
	failed := 0
	for _, g := range groups {
		if err := repair.Apply(ctx, pool, g); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "repair-charge-fragments: %s: %v\n", g.Keeper().ExternalID, err)
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "repair-charge-fragments: %d of %d sessions failed (each is atomic; the rest were applied)\n", failed, len(groups))
		return 1
	}
	fmt.Printf("applied: %d sessions merged, %d rows removed\n", len(groups), removed)
	return 0
}

func valOr0(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// writeFragmentsBackup writes every touched row (pieces and keepers)
// with the planned values, 0600, and syncs it before returning.
func writeFragmentsBackup(path string, groups []repair.Group) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := writeFragmentsCSV(f, groups); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeFragmentsCSV(w io.Writer, groups []repair.Group) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"role", "id", "external_id", "user_id", "username", "vehicle_id", "rivian_vehicle_id",
		"started_at", "ended_at", "start_soc_pct", "end_soc_pct", "energy_added_kwh", "max_power_kw", "active_seconds",
		"cost", "currency", "final_state", "cost_source", "source",
		"planned_started_at", "planned_energy_kwh", "planned_max_power_kw", "planned_active_seconds"}); err != nil {
		return err
	}
	fp := func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'f', -1, 64)
	}
	for _, g := range groups {
		m := g.Plan()
		keeper := g.Keeper()
		for _, p := range g.Pieces {
			role := "removed"
			planned := []string{"", "", "", ""}
			if p.ID == keeper.ID {
				role = "keeper"
				planned = []string{m.StartedAt.UTC().Format(time.RFC3339), fp(m.EnergyKWh), fp(m.MaxPowerKW), fp(m.ActiveSeconds)}
			}
			row := []string{role, p.ID.String(), p.ExternalID, p.UserID.String(), p.Username, p.VehicleID.String(), p.RivianID,
				p.StartedAt.UTC().Format(time.RFC3339Nano), p.EndedAt.UTC().Format(time.RFC3339Nano),
				fp(p.StartSoC), fp(p.EndSoC), fp(p.EnergyKWh), fp(p.MaxPowerKW), fp(p.ActiveSeconds),
				fp(p.Cost), p.Currency, p.FinalState, p.CostSource, p.Source}
			if err := cw.Write(append(row, planned...)); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}
