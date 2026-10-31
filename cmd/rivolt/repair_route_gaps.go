package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/repair/routegaps"
)

// runRepairRouteGaps fills the straight lines left in recorded drive routes by dropped GPS fixes (see internal/repair/routegaps):
// monitors the boot hydrate sweep started before #1356 never had the route filler. It only rewrites drives.route_polyline of closed
// live drives. The dry run prints counts only (no locations): drives scanned, drives with a dropout, dropouts, per month, and the
// sha256 of the sorted id list. --apply needs the approved dry run's --expect-count and --expect-sha (it aborts unless both match),
// a Valhalla URL, and --backup (a new 0600 CSV of every replaced route, written before each update); an update only applies if the
// stored route is still the one that was planned. Undo with --restore-csv FILE [--apply].
func runRepairRouteGaps(args []string) int {
	fs := flag.NewFlagSet("repair-route-gaps", flag.ContinueOnError)
	sinceS := fs.String("since", "", "first drive start, RFC3339 or YYYY-MM-DD (default: 30 days ago)")
	untilS := fs.String("until", "", "drive starts before this, RFC3339 or YYYY-MM-DD (default: now)")
	minGap := fs.Float64("min-gap-m", routegaps.DefaultMinGapM, "a dropout is two consecutive route points at least this many metres apart (and under 50 km)")
	apply := fs.Bool("apply", false, "rewrite the routes (default: dry run)")
	expectCount := fs.Int("expect-count", -1, "with --apply: the approved dry run's drive count (required)")
	expectSHA := fs.String("expect-sha", "", "with --apply: the approved dry run's sha256 of the sorted id list (required)")
	backup := fs.String("backup", "", "with --apply: a new CSV for the backup (required; must not exist)")
	valhalla := fs.String("valhalla-url", os.Getenv("RIVOLT_VALHALLA_BASE_URL"), "with --apply: the Valhalla base URL (default RIVOLT_VALHALLA_BASE_URL)")
	pace := fs.Duration("pace", 150*time.Millisecond, "with --apply: wait between drives")
	restoreCSV := fs.String("restore-csv", "", "undo an apply from its --backup CSV (dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *restoreCSV == "" && *apply {
		switch {
		case *expectCount < 0 || *expectSHA == "":
			fmt.Fprintln(os.Stderr, "repair-route-gaps: --apply requires --expect-count and --expect-sha from the approved dry run")
			return 2
		case *backup == "":
			fmt.Fprintln(os.Stderr, "repair-route-gaps: --apply requires --backup <csv path>")
			return 2
		case maps.NewValhalla(*valhalla) == nil:
			fmt.Fprintln(os.Stderr, "repair-route-gaps: --apply requires a Valhalla URL (--valhalla-url or RIVOLT_VALHALLA_BASE_URL)")
			return 2
		}
		// Every route coordinate in the window goes to this service: only this deployment's own may receive it.
		if err := routegaps.CheckValhallaURL(*valhalla); err != nil {
			fmt.Fprintln(os.Stderr, "repair-route-gaps:", err)
			return 2
		}
	}
	now := time.Now().UTC()
	since, err := parseRepairTime(*sinceS, now.AddDate(0, 0, -30))
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: --since:", err)
		return 2
	}
	until, err := parseRepairTime(*untilS, now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: --until:", err)
		return 2
	}
	if *minGap < 100 {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: --min-gap-m must be at least 100 (the recorder's own threshold)")
		return 2
	}

	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema.
	if err := db.RequireTables(ctx, pool, "drives"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps:", err)
		return 1
	}

	if *restoreCSV != "" {
		rows, err := readRouteGapsCSV(*restoreCSV)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-route-gaps:", err)
			return 1
		}
		fmt.Printf("%s: %d route(s) to restore\n", *restoreCSV, len(rows))
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		res, err := routegaps.Restore(ctx, pool, rows)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-route-gaps: restore:", err)
			return 1
		}
		fmt.Printf("restored %d route(s); skipped %d (changed since the fill)\n", res.Restored, res.Skipped)
		return 0
	}

	cands, err := routegaps.Select(ctx, pool, since, until)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: select:", err)
		return 1
	}
	plan := routegaps.MakePlan(cands, *minGap)
	ids := plan.IDs()
	digest := routegaps.Digest(ids)
	fmt.Printf("window: %s to %s\n", since.Format("2006-01-02"), until.Format("2006-01-02"))
	fmt.Printf("closed live drives with a stored route: %d\n", plan.Scanned)
	fmt.Printf("routes that could not be read: %d\n", plan.Undecoded)
	fmt.Printf("drives with at least one dropout (>= %.0f m): %d\n", plan.MinGapM, len(ids))
	fmt.Printf("dropouts: %d (estimated Valhalla calls: at most %d)\n", plan.GapsTotal, plan.GapsTotal)
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
		fmt.Fprintf(os.Stderr, "repair-route-gaps: NOT applied: the plan is %d drives / %s, the approved dry run said %d / %s\n", len(ids), digest, *expectCount, *expectSHA)
		return 1
	}

	bk, err := newRouteGapsCSV(*backup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: backup:", err)
		return 1
	}
	defer func() { _ = bk.f.Close() }() // error paths; the end checks the close below
	fmt.Printf("backup file: %s\n", *backup)
	res, err := routegaps.Apply(ctx, pool, plan, maps.NewValhalla(*valhalla), bk.write, *pace)
	fmt.Printf("rewritten %d route(s); %d dropout(s) filled, %d left as they were; %d drive(s) unchanged, %d changed meanwhile and left alone\n",
		res.Updated, res.Filled, res.Failed, res.Unchanged, res.Raced)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: stopped:", err)
		return 1
	}
	if err := bk.f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "repair-route-gaps: backup close:", err)
		return 1
	}
	return 0
}

// parseRepairTime reads RFC3339 or YYYY-MM-DD (UTC); empty is def.
func parseRepairTime(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither RFC3339 nor YYYY-MM-DD", s)
	}
	return t.UTC(), nil
}

// routeGapsCSV is the durable backup: one line per replaced route (the old route and the hash of the new one), 0600, exclusive
// create, flushed and synced after every row, so the line exists before the update that needs it.
type routeGapsCSV struct {
	f *os.File
	w *csv.Writer
}

func newRouteGapsCSV(path string) (*routeGapsCSV, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	c := &routeGapsCSV{f: f, w: csv.NewWriter(f)}
	_ = c.w.Write([]string{"drive_id", "user_id", "old_route", "new_route_sha256"})
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		return nil, err
	}
	return c, f.Sync()
}

func (c *routeGapsCSV) write(r routegaps.BackupRow) error {
	if err := c.w.Write([]string{r.DriveID.String(), r.UserID.String(), r.Old, r.NewSHA}); err != nil {
		return err
	}
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		return err
	}
	return c.f.Sync()
}

func readRouteGapsCSV(path string) ([]routegaps.BackupRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []routegaps.BackupRow
	for i, r := range recs {
		if i == 0 || len(r) < 4 {
			continue
		}
		did, err1 := uuid.Parse(r[0])
		uid, err2 := uuid.Parse(r[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s:%d: bad ids", path, i+1)
		}
		out = append(out, routegaps.BackupRow{DriveID: did, UserID: uid, Old: r[2], NewSHA: r[3]})
	}
	return out, nil
}
