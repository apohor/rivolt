package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/chargers"
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
func runReprice(args []string) int {
	fs := flag.NewFlagSet("reprice", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the changes (default: dry run)")
	user := fs.String("user", "", "only this username")
	idsPath := fs.String("ids", "", "only the charge ids in this file (one per line)")
	backupPath := fs.String("backup", "", "with --apply: CSV of each row before the update (required)")
	if err := fs.Parse(args); err != nil {
		return 2
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
		rate, _, network, ok := pricers[uid](lat, lon)
		if !ok {
			skip(c.id, "no DCFC station within range")
			continue
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
		fmt.Printf("%s  %s  %-24s %6.1f kWh  %5.0f kW  $%7.2f → $%7.2f  %s @ $%.2f/kWh\n",
			c.id, c.at.Format("2006-01-02 15:04"), c.user, c.kwh, c.kw, c.oldCost, c.newCost, c.network, c.rate)
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
		if _, err := tx.ExecContext(ctx, `
			UPDATE charges SET
			       -- 0, not NULL, for a row that had no price: the undo
			       -- selects on legacy_cost IS NOT NULL.
			       legacy_cost = COALESCE(cost, 0), legacy_price_per_kwh = COALESCE(price_per_kwh, 0),
			       cost = $2, price_per_kwh = $3, currency = 'USD',
			       cost_source = $4, network = NULLIF($5, ''), updated_at = NOW()
			WHERE id = $1 AND cost_source IS NULL`,
			c.id, c.newCost, c.rate, rivian.CostSourceNetworkEstimate, c.network); err != nil {
			fmt.Fprintln(os.Stderr, "reprice: update:", err)
			return 1
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
