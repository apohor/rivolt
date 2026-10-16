package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/r2"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// runRepairR2History rebuilds a vehicle's drives and charges in a
// window from its vehicle_state rows (internal/repair/r2), for R2s
// recorded before #214 whose drives never closed and whose charges were
// never recorded. Dry run by default; --apply needs --backup (a new
// CSV: every replaced row, then the ids it inserted) and runs in one
// transaction; --restore-csv undoes an apply. Prints no locations.
func runRepairR2History(args []string) int {
	fs := flag.NewFlagSet("repair-r2-history", flag.ContinueOnError)
	vehicle := fs.String("vehicle", "", "Rivian vehicle id, e.g. 01-247739360 (required)")
	sinceS := fs.String("since", "", "window start, RFC3339 (required)")
	untilS := fs.String("until", "", "window end, RFC3339, before the fixed release reached prod (required)")
	apply := fs.Bool("apply", false, "write the plan (default: dry run)")
	backup := fs.String("backup", "", "with --apply: new CSV for the backup (required)")
	restoreCSV := fs.String("restore-csv", "", "undo an apply from its --backup CSV (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *vehicle == "" {
		fmt.Fprintln(os.Stderr, "repair-r2-history: --vehicle is required")
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "vehicles", "vehicle_state", "drives", "charges",
		"drive_recaps", "drive_efficiency", "drive_weather", "drive_weather_series"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history:", err)
		return 1
	}
	v, err := r2.LoadVehicle(ctx, pool, *vehicle)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history:", err)
		return 1
	}

	if *restoreCSV != "" {
		f, err := os.Open(*restoreCSV)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-r2-history:", err)
			return 1
		}
		rows, err := readR2CSV(f)
		_ = f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-r2-history:", err)
			return 1
		}
		fmt.Printf("%s: %d row(s) to put back or remove\n", *restoreCSV, len(rows))
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		if err := r2.Restore(ctx, pool, v, rows); err != nil {
			fmt.Fprintln(os.Stderr, "repair-r2-history: restore:", err)
			return 1
		}
		fmt.Println("restored")
		return 0
	}

	since, err1 := time.Parse(time.RFC3339, *sinceS)
	until, err2 := time.Parse(time.RFC3339, *untilS)
	if err1 != nil || err2 != nil || !until.After(since) {
		fmt.Fprintln(os.Stderr, "repair-r2-history: --since/--until must be RFC3339, since < until")
		return 2
	}
	if *apply && *backup == "" {
		fmt.Fprintln(os.Stderr, "repair-r2-history: --apply needs --backup FILE")
		return 2
	}
	rows, err := r2.LoadRows(ctx, pool, v, since, until)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: samples:", err)
		return 1
	}
	drives, err := r2.LoadDrives(ctx, pool, v, since, until)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: drives:", err)
		return 1
	}
	stored, err := r2.LoadCharges(ctx, pool, v, since, until)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: charges:", err)
		return 1
	}
	trips, chs := r2.Segment(rows)
	// AC charges at home get the home rate, as the recorder stamps them;
	// away (or no home/rate) stays unpriced.
	ss := settings.NewFactory(pool).For(v.UserID)
	pr := r2.Pricing{HomeRadiusM: rivian.HomeRadiusM, DCFCMinKW: rivian.DCFCMinKW}
	if cfg, err := settings.GetChargingConfig(ctx, ss); err == nil {
		pr.HomeRate, pr.Currency = cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	if h, err := settings.GetHomeLocation(ctx, ss); err == nil && h.Set {
		pr.HomeKnown, pr.HomeLat, pr.HomeLon = true, h.Latitude, h.Longitude
	}
	chs = r2.PriceCharges(chs, pr)
	plan, err := r2.MakePlan(v, since, until, trips, chs, drives, stored)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: refusing:", err)
		return 1
	}
	printR2Plan(os.Stdout, plan, len(rows))
	if !*apply {
		fmt.Println("\ndry run: nothing written (pass --apply --backup FILE)")
		return 0
	}

	f, err := os.OpenFile(*backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: backup:", err)
		return 1
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"kind", "row_json"})
	sink := func(rs []r2.BackupRow) error {
		for _, r := range rs {
			if err := w.Write([]string{r.Kind, string(r.Row)}); err != nil {
				return err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		return f.Sync()
	}
	if err := r2.Apply(ctx, pool, plan, rivian.EncodePolyline, sink); err != nil {
		fmt.Fprintln(os.Stderr, "repair-r2-history: apply (rolled back):", err)
		return 1
	}
	fmt.Printf("applied: %d drive(s) replaced by %d, %d charge(s) added; backup %s (undo: --restore-csv)\n",
		len(plan.DeleteDrives), len(plan.InsertDrives), len(plan.InsertCharges), *backup)
	return 0
}

func printR2Plan(w io.Writer, p r2.Plan, nRows int) {
	fmt.Fprintf(w, "%s  %s → %s  (%d samples)\n", p.Vehicle.RivianID,
		p.Since.UTC().Format(time.RFC3339), p.Until.UTC().Format(time.RFC3339), nRows)
	var oldMi float64
	fmt.Fprintf(w, "\nreplace %d drive(s):\n", len(p.DeleteDrives))
	for _, d := range p.DeleteDrives {
		oldMi += d.Miles
		fmt.Fprintf(w, "  %s  %s → %s  %8.1f mi  (%s)\n", d.ExternalID,
			d.Start.UTC().Format("2006-01-02 15:04"), d.End.UTC().Format("2006-01-02 15:04"), d.Miles, d.End.Sub(d.Start).Round(time.Minute))
	}
	var newMi, kwh float64
	fmt.Fprintf(w, "\nwith %d trip(s):\n", len(p.InsertDrives))
	for _, t := range p.InsertDrives {
		newMi += t.DistanceMi()
		fmt.Fprintf(w, "  %s → %s  %6.1f mi  %5.1f kWh  max %3.0f mph\n",
			t.Start.UTC().Format("2006-01-02 15:04"), t.End.UTC().Format("15:04"), t.DistanceMi(), t.EnergyKWh(p.Vehicle.PackKWh), t.MaxSpeedMph)
	}
	fmt.Fprintf(w, "\nadd %d charge(s):\n", len(p.InsertCharges))
	for _, c := range p.InsertCharges {
		kwh += c.EnergyKWh
		price := "unpriced"
		if c.CostSource != "" {
			price = fmt.Sprintf("$%.2f %s @ %.3f/kWh", c.Cost, c.CostSource, c.PricePerKWh)
		}
		fmt.Fprintf(w, "  %s → %s  %5.1f kWh  peak %4.1f kW  SoC %.0f→%.0f%%  place %-4s  %s\n",
			c.Start.UTC().Format("2006-01-02 15:04"), c.End.UTC().Format("15:04"), c.EnergyKWh, c.PeakKW, c.StartSoC, c.EndSoC, c.Place, price)
	}
	for _, s := range p.Skipped {
		fmt.Fprintf(w, "  skipped: %s\n", s)
	}
	fmt.Fprintf(w, "\ntotals: drives %d → %d (%.1f → %.1f mi), charges +%d (%.1f kWh)\n",
		len(p.DeleteDrives), len(p.InsertDrives), oldMi, newMi, len(p.InsertCharges), kwh)
}

func readR2CSV(r io.Reader) ([]r2.BackupRow, error) {
	recs, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 || len(recs[0]) != 2 || recs[0][0] != "kind" {
		return nil, fmt.Errorf("not a repair-r2-history backup")
	}
	out := make([]r2.BackupRow, 0, len(recs)-1)
	for i, rec := range recs[1:] {
		if len(rec) != 2 || rec[0] == "" {
			return nil, fmt.Errorf("line %d: malformed", i+2)
		}
		out = append(out, r2.BackupRow{Kind: rec[0], Row: []byte(rec[1])})
	}
	return out, nil
}
