package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/phantom"
)

// parseIDFile reads charge ids, one per line (first comma/space
// separated token; '#' comments and blanks ignored). Duplicates are
// dropped; any unparsable line is an error (the list is audited, so a
// typo must not silently shrink it).
func parseIDFile(path string) ([]uuid.UUID, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		tok := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '|' })[0]
		id, err := uuid.Parse(tok)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %q is not a charge id", path, n, tok)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, sc.Err()
}

// phantomCSV is the durable backup: one line per backed-up row with
// the full row as JSON, so --restore-csv can undo without the backup
// table (production may not have migration 0049). 0600, exclusive
// create, fsynced after each vehicle's batch.
type phantomCSV struct {
	f *os.File
	w *csv.Writer
}

func newPhantomCSV(path string) (*phantomCSV, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	c := &phantomCSV{f: f, w: csv.NewWriter(f)}
	_ = c.w.Write([]string{"batch_id", "kind", "charge_id", "user_id", "row_json"})
	c.w.Flush()
	return c, c.w.Error()
}

func (c *phantomCSV) sink(batch uuid.UUID) func([]phantom.BackupRow) error {
	return func(rows []phantom.BackupRow) error {
		for _, r := range rows {
			if err := c.w.Write([]string{batch.String(), r.Kind, r.ChargeID.String(), r.UserID.String(), string(r.Row)}); err != nil {
				return err
			}
		}
		c.w.Flush()
		if err := c.w.Error(); err != nil {
			return err
		}
		return c.f.Sync()
	}
}

func (c *phantomCSV) Close() error { return c.f.Close() }

func readPhantomCSV(path string) ([]phantom.BackupRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []phantom.BackupRow
	for i, r := range recs {
		if i == 0 || len(r) < 5 {
			continue
		}
		cid, err1 := uuid.Parse(r[2])
		uid, err2 := uuid.Parse(r[3])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s:%d: bad ids", path, i+1)
		}
		out = append(out, phantom.BackupRow{Kind: r[1], ChargeID: cid, UserID: uid, Row: []byte(r[4])})
	}
	return out, nil
}

// runRepairPhantoms deletes phantom live charges from an audited id
// list. Dry run unless --apply; --apply needs --backup (CSV) and
// writes charges_deleted_backup rows in the same transaction as each
// vehicle's deletes. --restore <batch> undoes a batch.
func runRepairPhantoms(args []string) int {
	fs := flag.NewFlagSet("repair-phantom-charges", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "delete the rows (default: dry run)")
	idsPath := fs.String("ids", "", "file of audited charge ids, one per line (required)")
	backup := fs.String("backup", "", "CSV backup path (required with --apply; must not exist)")
	user := fs.String("user", "", "username; ids owned by other users are skipped (required with --apply: approval is per user)")
	restore := fs.String("restore", "", "undo a batch id from charges_deleted_backup (dry run unless --apply)")
	restoreCSV := fs.String("restore-csv", "", "undo from a --backup CSV (works without the backup table; dry run unless --apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges: postgres:", err)
		return 1
	}
	defer pool.Close()
	// Never migrate: a newer binary must not change prod's schema.
	if err := db.RequireTables(ctx, pool, "charges", "vehicles", "vehicle_state", "vehicle_pack_health_samples", "charge_restamps"); err != nil {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
		return 1
	}
	hasBackupTable, err := phantom.BackupTableExists(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
		return 1
	}
	if hasBackupTable {
		fmt.Println("backup table charges_deleted_backup present: backing up to it and the CSV")
	} else {
		fmt.Println("backup table charges_deleted_backup absent (migration 0049 not applied): CSV is the only backup; undo with --restore-csv")
	}

	if *restoreCSV != "" {
		rows, err := readPhantomCSV(*restoreCSV)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
			return 1
		}
		fmt.Printf("%s: %d row(s) to restore\n", *restoreCSV, len(rows))
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		res, err := phantom.RestoreRows(ctx, pool, rows)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges: restore:", err)
			return 1
		}
		fmt.Printf("restored %d charge(s), %d pack-health sample(s), %d restamp(s)\n", res.Charges, res.PackHealth, res.Restamps)
		return 0
	}

	if *restore != "" {
		if !hasBackupTable {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges: --restore needs the backup table; use --restore-csv")
			return 2
		}
		bid, err := uuid.Parse(*restore)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges: --restore needs a batch uuid")
			return 2
		}
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges_deleted_backup WHERE batch_id = $1 AND restored_at IS NULL`, bid).Scan(&n); err != nil {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
			return 1
		}
		fmt.Printf("batch %s: %d backed-up row(s) to restore\n", bid, n)
		if !*apply {
			fmt.Println("dry run; pass --apply to restore")
			return 0
		}
		res, err := phantom.Restore(ctx, pool, bid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges: restore:", err)
			return 1
		}
		fmt.Printf("restored %d charge(s), %d pack-health sample(s), %d restamp(s)\n", res.Charges, res.PackHealth, res.Restamps)
		return 0
	}

	if *idsPath == "" {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges: --ids is required (the audited list); this command never searches for candidates")
		return 2
	}
	if *apply && *user == "" {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges: --apply requires --user (owner approval is per user)")
		return 2
	}
	if *apply && *backup == "" {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges: --apply requires --backup <csv path>")
		return 2
	}
	ids, err := parseIDFile(*idsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
		return 2
	}
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges: id file is empty")
		return 2
	}

	// Owners of the ids (rows are always loaded user-scoped).
	owners := map[uuid.UUID][]uuid.UUID{}
	found := 0
	idArr := make([]string, len(ids))
	for i, id := range ids {
		idArr[i] = id.String()
	}
	orows, err := pool.QueryContext(ctx, `SELECT c.id, c.user_id FROM charges c JOIN users u ON u.id = c.user_id
		  WHERE c.id = ANY($1::uuid[]) AND ($2 = '' OR lower(u.username) = lower($2))`, "{"+strings.Join(idArr, ",")+"}", *user)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
		return 1
	}
	for orows.Next() {
		var cid, uid uuid.UUID
		if err := orows.Scan(&cid, &uid); err != nil {
			orows.Close()
			fmt.Fprintln(os.Stderr, "repair-phantom-charges:", err)
			return 1
		}
		owners[uid] = append(owners[uid], cid)
		found++
	}
	orows.Close()
	if found != len(ids) {
		fmt.Printf("WARNING: %d of %d id(s) not found (already deleted, wrong id, or another user)\n", len(ids)-found, len(ids))
	}

	var all []phantom.Row
	verdicts := map[uuid.UUID]phantom.Verdict{}
	for uid, cids := range owners {
		rows, err := phantom.Load(ctx, pool, uid, cids)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair-phantom-charges: load:", err)
			return 1
		}
		for _, r := range rows {
			verdicts[r.ID] = phantom.Classify(r)
		}
		all = append(all, rows...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].VehicleID != all[j].VehicleID {
			return all[i].VehicleID.String() < all[j].VehicleID.String()
		}
		return all[i].StartedAt.Before(all[j].StartedAt)
	})

	var del []phantom.Row
	var delKWh float64
	fmt.Println("charge_id                              vehicle        started_at (UTC)     kWh    soc%   power_kWh  verdict")
	for _, r := range all {
		v := verdicts[r.ID]
		soc := "n/a"
		if r.StartSoC != nil && r.EndSoC != nil {
			soc = fmt.Sprintf("%+.1f", *r.EndSoC-*r.StartSoC)
		}
		out := "DELETE"
		if !v.Delete {
			out = "skip: " + v.Reason
		} else {
			del = append(del, r)
			delKWh += r.EnergyKWh
		}
		fmt.Printf("%s  %-13s  %s  %6.1f  %6s  %8.2f  %s\n", r.ID, r.RivianID, r.StartedAt.Format("2006-01-02 15:04:05"), r.EnergyKWh, soc, r.PowerKWh, out)
	}
	fmt.Printf("\n%d id(s) given, %d to delete (%.1f kWh), %d skipped\n", len(ids), len(del), delKWh, len(all)-len(del))
	for _, m := range phantom.Summarise(all, verdicts) {
		fmt.Printf("  %s: %d row(s), %.1f kWh, cost %.2f removed from the counted totals\n", m.Month, m.Rows, m.KWh, m.Cost)
	}
	if !*apply {
		fmt.Println("dry run; nothing written. Pass --apply --backup <csv> to delete.")
		return 0
	}
	if len(del) == 0 {
		fmt.Println("nothing to delete")
		return 0
	}

	batch := uuid.New()
	bk, err := newPhantomCSV(*backup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair-phantom-charges: backup:", err)
		return 1
	}
	defer bk.Close()
	fmt.Printf("backup file: %s (batch %s)\n", *backup, batch)

	byVehicle := map[uuid.UUID][]phantom.Row{}
	var vorder []uuid.UUID
	for _, r := range del {
		if _, ok := byVehicle[r.VehicleID]; !ok {
			vorder = append(vorder, r.VehicleID)
		}
		byVehicle[r.VehicleID] = append(byVehicle[r.VehicleID], r)
	}
	failed := 0
	for _, vid := range vorder {
		rs := byVehicle[vid]
		cids := make([]uuid.UUID, len(rs))
		for i, r := range rs {
			cids[i] = r.ID
		}
		n, err := phantom.Apply(ctx, pool, batch, rs[0].UserID, vid, cids, "phantom stale-copy charge (audited id list)", bk.sink(batch))
		if err != nil {
			failed++
			fmt.Printf("vehicle %s: NOT applied: %v\n", rs[0].RivianID, err)
			continue
		}
		fmt.Printf("vehicle %s: deleted %d row(s)\n", rs[0].RivianID, n)
	}
	fmt.Printf("undo: rivolt repair-phantom-charges --restore-csv %s --apply\n", *backup)
	if failed > 0 {
		return 1
	}
	return 0
}
