package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// runReprice re-prices DC fast charges the recorder stored at the
// user's home rate before migration 0040 — the only legacy rows it
// touches. Other legacy rows keep cost_source NULL and the ledger
// derives their source at read time (an old row can't distinguish a
// Rivian bill from a user edit or an older home rate, so nothing is
// stamped on a guess).
//
// A row qualifies when its cost is at the current home rate (or unset),
// it was a real DC fast charge (rivian.IsDCFCSession: peak AND effective
// power, so glitched home-L2 peaks don't count), and the nearest NREL
// DCFC station gives a network rate. No station → left alone.
//
// Dry run by default: prints each change (date, energy, power, old →
// new cost, network; never locations) and per-user totals. --apply
// writes them in one transaction, keeping the old cost and rate in
// legacy_cost / legacy_price_per_kwh (undo: migration 0040's comment),
// and refuses to run without the chargers archive.
//
// --ids FILE limits it to an audited list of charge ids (one per line,
// as repair-phantom-charges reads them): the dry run accounts for every
// id, and --apply refuses unless every one of them would be re-priced.
// --apply needs --backup FILE: each row as it was (to_jsonb), written
// 0600 and fsynced before the update transaction starts.
//
// --detect classifies with DC fast charge network detection (the
// recorder's, contract dcfc-auto-network v1) instead of the nearest
// station: the user's own verified charge at the site, then every
// station within 250 m. It writes network_state / network_confidence /
// network_evidence too (migration 0051 required), so co-located sites
// land as needs_confirmation for the user to settle. --min-confidence
// keeps only rows detected at least that sure (0.85 = estimates only).
// A row nothing matched is skipped. Undo as 0040's comment, plus
// network_state, network_confidence and network_evidence = NULL.
//
// --restore-csv FILE undoes an apply from that backup (exact: price
// columns, currency, source, network, legacy_*, the verify state and
// updated_at).
func runReprice(args []string) int {
	fs := flag.NewFlagSet("reprice", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the changes (default: dry run)")
	user := fs.String("user", "", "only this username")
	idsPath := fs.String("ids", "", "only the charge ids in this file (one per line)")
	backupPath := fs.String("backup", "", "with --apply: CSV of each row before the update (required)")
	detect := fs.Bool("detect", false, "classify with network detection and write the verify state (needs migration 0051)")
	minConf := fs.Float64("min-confidence", 0, "with --detect: only rows detected at least this confident (0.85 = estimates only)")
	restorePath := fs.String("restore-csv", "", "undo: put back every row in this --backup CSV exactly as it was")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *restorePath != "" {
		return runRepriceRestore(*restorePath, *apply)
	}
	if *apply && *backupPath == "" {
		fmt.Fprintln(os.Stderr, "reprice: --apply needs --backup FILE")
		return 2
	}
	var wantIDs []uuid.UUID
	if *idsPath != "" {
		var err error
		if wantIDs, err = parseIDFile(*idsPath); err != nil {
			fmt.Fprintln(os.Stderr, "reprice: --ids:", err)
			return 2
		}
		if len(wantIDs) == 0 {
			fmt.Fprintln(os.Stderr, "reprice: --ids: no ids in", *idsPath)
			return 2
		}
	}
	idStrs := make([]string, len(wantIDs))
	for i, id := range wantIDs {
		idStrs[i] = id.String()
	}
	idArr := "{" + strings.Join(idStrs, ",") + "}"
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "charges", "users"); err != nil {
		fmt.Fprintln(os.Stderr, "reprice:", err)
		return 1
	}
	if *detect {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'charges' AND column_name IN ('network_state', 'network_confidence', 'network_evidence')`).Scan(&n); err != nil || n != 3 {
			fmt.Fprintln(os.Stderr, "reprice: --detect needs migration 0051 (charges.network_state); this database doesn't have it")
			return 1
		}
	}

	var archive *chargers.Archive
	if base := strings.TrimSpace(os.Getenv("RIVOLT_TILES_BASE_URL")); base != "" {
		archive = chargers.New(strings.TrimRight(base, "/") + "/chargers.pmtiles")
		lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := archive.Reload(lctx)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "reprice: chargers archive:", err)
			archive = nil
		}
	}
	if archive == nil {
		if *apply {
			fmt.Fprintln(os.Stderr, "reprice: refusing --apply without the chargers archive (RIVOLT_TILES_BASE_URL)")
			return 1
		}
		fmt.Fprintln(os.Stderr, "reprice: no chargers archive; no station can match, so nothing will be re-priced")
	}

	rows, err := pool.QueryContext(ctx, `
		SELECT c.id, c.user_id, u.username, c.started_at, c.ended_at,
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.max_power_kw,0),
		       COALESCE(c.active_seconds,0)::float8,
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       COALESCE(c.cost,0)::float8, COALESCE(c.price_per_kwh,0)::float8
		FROM charges c JOIN users u ON u.id = c.user_id
		WHERE c.cost_source IS NULL AND COALESCE(c.max_power_kw,0) >= $2
		  AND ($1 = '' OR lower(u.username) = lower($1))
		  AND (cardinality($3::uuid[]) = 0 OR c.id = ANY($3::uuid[]))
		ORDER BY u.username, c.started_at`, *user, rivian.DCFCMinKW, idArr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: query:", err)
		return 1
	}
	var changes []repriceChange
	skipped := map[string]int{}
	// With --ids, every id is accounted for: re-priced, skipped (why),
	// or not selected at all (absent, already sourced, below DCFC kW).
	outcome := map[string]string{}
	skip := func(id, reason string) {
		skipped[reason]++
		outcome[id] = "skipped: " + reason
	}
	sf := settings.NewFactory(pool)
	pricers := map[uuid.UUID]rivian.DCFCPriceLookup{}
	detectors := map[uuid.UUID]rivian.DCFCDetector{}
	cf := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	always := func() bool { return true }
	homeRates := map[uuid.UUID]float64{}
	for rows.Next() {
		var (
			c             repriceChange
			uid           uuid.UUID
			ended         time.Time
			active        float64
			lat, lon, ppk float64
		)
		if err := rows.Scan(&c.id, &uid, &c.user, &c.at, &ended, &c.kwh, &c.kw, &active, &lat, &lon, &c.oldCost, &ppk); err != nil {
			fmt.Fprintln(os.Stderr, "reprice: scan:", err)
			return 1
		}
		if _, ok := pricers[uid]; !ok {
			ss := sf.For(uid)
			pricers[uid] = newDCFCPricer(archive, ss)
			detectors[uid] = newDCFCDetector(archive, ss, cf.For(uid), always)
			if ss != nil {
				if cfg, err := settings.GetChargingConfig(ctx, ss); err == nil {
					homeRates[uid] = cfg.HomePricePerKWh
				}
			}
		}
		home := homeRates[uid]
		if !(c.oldCost == 0 || (home > 0 && math.Abs(ppk-home) < 0.0005)) {
			skip(c.id, "priced other than home rate")
			continue
		}
		if !rivian.IsDCFCSession(c.kw, c.kwh, active, ended.Sub(c.at)) {
			skip(c.id, "peak glitch (effective power below DCFC)")
			continue
		}
		var (
			rate    float64
			network string
		)
		if *detect {
			v, _ := detectors[uid](lat, lon, false, c.kw)
			if !v.Priced {
				skip(c.id, "no network detected (left for the user)")
				continue
			}
			if v.Confidence < *minConf {
				skip(c.id, fmt.Sprintf("detected below --min-confidence (%s)", v.State))
				continue
			}
			rate, network = v.Rate, v.Network
			c.state, c.conf, c.evidence = v.State, v.Confidence, v.Evidence
		} else {
			r, _, n, ok := pricers[uid](lat, lon)
			if !ok {
				skip(c.id, "no DCFC station within range")
				continue
			}
			rate, network = r, n
		}
		c.oldRate, c.rate, c.newCost, c.network = ppk, rate, rate*c.kwh, network
		changes = append(changes, c)
		outcome[c.id] = "re-price"
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "reprice: rows:", err)
		return 1
	}
	_ = rows.Close()

	totals := map[string][2]float64{}
	for _, c := range changes {
		t := totals[c.user]
		t[0] += c.oldCost
		t[1] += c.newCost
		totals[c.user] = t
		verdict := ""
		if c.state != "" {
			verdict = fmt.Sprintf("  [%s %.2f]", c.state, c.conf)
		}
		fmt.Printf("%s  %s  %-24s %6.1f kWh  %5.0f kW  $%7.2f → $%7.2f  %s @ $%.2f/kWh%s\n",
			c.id, c.at.Format("2006-01-02 15:04"), c.user, c.kwh, c.kw, c.oldCost, c.newCost, c.network, c.rate, verdict)
	}
	unpriced := repriceUnaccounted(idStrs, outcome)
	for _, line := range unpriced {
		fmt.Println(line)
	}
	missing := len(unpriced)
	fmt.Printf("\n%d DC fast charges to re-price\n", len(changes))
	for reason, n := range skipped {
		fmt.Printf("  skipped %d: %s\n", n, reason)
	}
	for u, t := range totals {
		fmt.Printf("  %s: $%.2f → $%.2f (%+.2f)\n", u, t[0], t[1], t[1]-t[0])
	}
	if !*apply {
		fmt.Println("\ndry run: nothing written (pass --apply to write)")
		return 0
	}
	if missing > 0 {
		fmt.Fprintf(os.Stderr, "reprice: refusing --apply: %d of the %d listed ids would not be re-priced (see above)\n", missing, len(idStrs))
		return 1
	}
	if len(changes) == 0 {
		fmt.Println("nothing to apply")
		return 0
	}
	if err := writeRepriceBackup(ctx, pool, *backupPath, changes); err != nil {
		fmt.Fprintln(os.Stderr, "reprice: backup:", err)
		return 1
	}
	fmt.Printf("backup: %d rows written to %s\n", len(changes), *backupPath)
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: begin:", err)
		return 1
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range changes {
		res, err := tx.ExecContext(ctx, `
			UPDATE charges SET
			       -- 0, not NULL, for a row that had no price: the undo
			       -- selects on legacy_cost IS NOT NULL.
			       legacy_cost = COALESCE(cost, 0), legacy_price_per_kwh = COALESCE(price_per_kwh, 0),
			       cost = $2, price_per_kwh = $3, currency = 'USD',
			       cost_source = $4, network = NULLIF($5, ''), updated_at = NOW()
			WHERE id = $1 AND cost_source IS NULL`,
			c.id, c.newCost, c.rate, rivian.CostSourceNetworkEstimate, c.network)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reprice: update:", err)
			return 1
		}
		// The guard (cost_source IS NULL) skips a row that changed since
		// the dry run; that must abort, not count as applied.
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			fmt.Fprintf(os.Stderr, "reprice: %s changed since the dry run (updated %d rows); nothing applied\n", c.id, n)
			return 1
		}
		if c.state != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE charges SET network_state = $2, network_confidence = $3, network_evidence = $4::jsonb
				WHERE id = $1`, c.id, c.state, c.conf, string(c.evidence)); err != nil {
				fmt.Fprintln(os.Stderr, "reprice: update state:", err)
				return 1
			}
		}
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "reprice: commit:", err)
		return 1
	}
	fmt.Printf("applied %d updates (old values kept in legacy_cost / legacy_price_per_kwh)\n", len(changes))
	return 0
}

// repriceChange is one row reprice would re-price.
type repriceChange struct {
	id                        string
	user                      string
	at                        time.Time
	kwh, kw, oldCost, newCost float64
	oldRate, rate             float64
	network                   string
	// --detect only: the verify state written with the price.
	state    string
	conf     float64
	evidence []byte
}

// repriceUnaccounted lists, one line each, the --ids entries that won't
// be re-priced: skipped (with the reason) or never selected.
func repriceUnaccounted(ids []string, outcome map[string]string) []string {
	var out []string
	for _, id := range ids {
		switch o := outcome[id]; {
		case o == "":
			out = append(out, id+"  not selected (no such charge, already has a cost source, or below DCFC kW)")
		case o != "re-price":
			out = append(out, id+"  "+o)
		}
	}
	return out
}

// repriceBackupRow is one charge as it was before re-pricing.
type repriceBackupRow struct {
	ChargeID, UserID string
	Row              []byte // to_jsonb(charges.*)
}

// writeRepriceBackup saves each row about to be re-priced, as it is now,
// to a new 0600 CSV, fsynced before the caller opens the update
// transaction.
func writeRepriceBackup(ctx context.Context, pool *sql.DB, path string, changes []repriceChange) error {
	ids := make([]string, len(changes))
	for i, c := range changes {
		ids[i] = c.id
	}
	rows, err := pool.QueryContext(ctx,
		`SELECT c.id, c.user_id, to_jsonb(c.*) FROM charges c WHERE c.id = ANY($1::uuid[]) ORDER BY c.id`,
		"{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return err
	}
	defer rows.Close()
	var backup []repriceBackupRow
	for rows.Next() {
		var r repriceBackupRow
		if err := rows.Scan(&r.ChargeID, &r.UserID, &r.Row); err != nil {
			return err
		}
		backup = append(backup, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(backup) != len(changes) {
		return fmt.Errorf("backed up %d rows, expected %d", len(backup), len(changes))
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeRepriceCSV(f, backup); err != nil {
		return err
	}
	return f.Sync()
}

// writeRepriceCSV writes the backup rows under a header.
func writeRepriceCSV(w io.Writer, rows []repriceBackupRow) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"charge_id", "user_id", "row_json"})
	for _, r := range rows {
		if err := cw.Write([]string{r.ChargeID, r.UserID, string(r.Row)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// readRepriceCSV reads a --backup file.
func readRepriceCSV(r io.Reader) ([]repriceBackupRow, error) {
	recs, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 || strings.Join(recs[0], ",") != "charge_id,user_id,row_json" {
		return nil, fmt.Errorf("not a reprice backup (header %q)", recs)
	}
	out := make([]repriceBackupRow, 0, len(recs)-1)
	for i, r := range recs[1:] {
		if len(r) != 3 {
			return nil, fmt.Errorf("line %d: %d fields", i+2, len(r))
		}
		if _, err := uuid.Parse(r[0]); err != nil {
			return nil, fmt.Errorf("line %d: charge id %q", i+2, r[0])
		}
		if _, err := uuid.Parse(r[1]); err != nil {
			return nil, fmt.Errorf("line %d: user id %q", i+2, r[1])
		}
		out = append(out, repriceBackupRow{ChargeID: r[0], UserID: r[1], Row: []byte(r[2])})
	}
	return out, nil
}

// runRepriceRestore puts every backed-up row's pricing back exactly as
// it was, from its row_json, in one transaction. A row that is no
// longer re-priced (the user edited it since, or it was already
// restored) aborts the whole run: nothing is overwritten silently.
func runRepriceRestore(path string, apply bool) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: restore:", err)
		return 1
	}
	rows, err := readRepriceCSV(f)
	_ = f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: restore:", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "reprice: restore: no rows in", path)
		return 1
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: postgres:", err)
		return 1
	}
	defer pool.Close()
	if !apply {
		// Dry run: each row as it is now → as the backup has it.
		for _, r := range rows {
			var was struct {
				Cost       *float64 `json:"cost"`
				CostSource *string  `json:"cost_source"`
			}
			_ = json.Unmarshal(r.Row, &was)
			var nowCost sql.NullFloat64
			var nowSource sql.NullString
			err := pool.QueryRowContext(ctx, `SELECT cost::float8, cost_source FROM charges WHERE id = $1::uuid AND user_id = $2::uuid`,
				r.ChargeID, r.UserID).Scan(&nowCost, &nowSource)
			state := ""
			if err != nil {
				state = "  (not found)"
			} else if nowSource.String != "network_estimate" {
				state = "  (no longer re-priced: restore would refuse)"
			}
			fmt.Printf("%s  now $%.2f %s → $%s %s%s\n", r.ChargeID, nowCost.Float64, nowSource.String, fmtCost(was.Cost), deref(was.CostSource), state)
		}
		fmt.Printf("\ndry run: %d rows; pass --apply to restore\n", len(rows))
		return 0
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: begin:", err)
		return 1
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		res, err := tx.ExecContext(ctx, `
			UPDATE charges c SET
			       cost = o.cost, price_per_kwh = o.price_per_kwh, currency = o.currency,
			       cost_source = o.cost_source, network = o.network,
			       legacy_cost = o.legacy_cost, legacy_price_per_kwh = o.legacy_price_per_kwh,
			       network_state = o.network_state, network_confidence = o.network_confidence,
			       network_evidence = o.network_evidence,
			       updated_at = o.updated_at
			FROM jsonb_populate_record(NULL::charges, $3::jsonb) o
			WHERE c.id = $1::uuid AND c.user_id = $2::uuid AND o.id = c.id
			  AND c.cost_source = 'network_estimate'`, r.ChargeID, r.UserID, string(r.Row))
		if err != nil {
			fmt.Fprintln(os.Stderr, "reprice: restore:", err)
			return 1
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			fmt.Fprintf(os.Stderr, "reprice: restore: %s is no longer re-priced (edited since, or already restored); nothing restored\n", r.ChargeID)
			return 1
		}
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "reprice: restore: commit:", err)
		return 1
	}
	fmt.Printf("restored %d rows from %s\n", len(rows), path)
	return 0
}

func fmtCost(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *p)
}

func deref(p *string) string {
	if p == nil {
		return "(none)"
	}
	return *p
}
