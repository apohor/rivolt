package main

import (
	"context"
	"flag"
	"fmt"
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

// runReprice backfills charges.cost_source (migration 0040) for legacy
// rows and re-prices DC fast charges that were stored at the home rate.
// Dry run by default: it prints every row it would change (dates,
// energy, power, old → new cost; never locations) and a total per user.
// --apply writes the changes in one transaction.
//
// Legacy rows are classified from what was stored:
//   - priced at the user's home rate (or unpriced) and peak >= 50 kW →
//     the bug: re-priced via the network pricer, network_estimate;
//   - priced at the home rate, slower → home_rate, cost unchanged;
//   - any other price → rivian (billed, or a user edit; the old schema
//     can't tell them apart), cost unchanged.
func runReprice(args []string) int {
	fs := flag.NewFlagSet("reprice", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the changes (default: dry run)")
	user := fs.String("user", "", "only this username")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: postgres:", err)
		return 1
	}
	defer pool.Close()

	var archive *chargers.Archive
	if base := strings.TrimSpace(os.Getenv("RIVOLT_TILES_BASE_URL")); base != "" {
		archive = chargers.New(strings.TrimRight(base, "/") + "/chargers.pmtiles")
		lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := archive.Reload(lctx)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "reprice: chargers archive:", err, "(unmatched DCFC rate will be used)")
			archive = nil
		}
	} else {
		fmt.Fprintln(os.Stderr, "reprice: RIVOLT_TILES_BASE_URL unset; unmatched DCFC rate will be used")
	}

	rows, err := pool.QueryContext(ctx, `
		SELECT c.id, c.user_id, u.username, c.started_at,
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.max_power_kw,0),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       COALESCE(c.cost,0)::float8, COALESCE(c.price_per_kwh,0)::float8
		FROM charges c JOIN users u ON u.id = c.user_id
		WHERE c.cost_source IS NULL AND ($1 = '' OR lower(u.username) = lower($1))
		ORDER BY u.username, c.started_at`, *user)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: query:", err)
		return 1
	}
	type change struct {
		id                        string
		user                      string
		at                        time.Time
		kwh, kw, oldCost, newCost float64
		rate                      float64
		source, network           string
	}
	var changes []change
	sf := settings.NewFactory(pool)
	pricers := map[uuid.UUID]rivian.DCFCPriceLookup{}
	homeRates := map[uuid.UUID]float64{}
	for rows.Next() {
		var (
			c             change
			uid           uuid.UUID
			lat, lon, ppk float64
		)
		if err := rows.Scan(&c.id, &uid, &c.user, &c.at, &c.kwh, &c.kw, &lat, &lon, &c.oldCost, &ppk); err != nil {
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
		atHomeRate := c.oldCost == 0 || (home > 0 && math.Abs(ppk-home) < 0.0005)
		c.newCost, c.rate = c.oldCost, ppk
		switch {
		case atHomeRate && c.kw >= rivian.DCFCMinKW && c.kwh > 0:
			rate, _, network, ok := pricers[uid](lat, lon)
			if !ok {
				continue
			}
			c.rate, c.newCost = rate, rate*c.kwh
			c.source, c.network = rivian.CostSourceNetworkEstimate, network
		case atHomeRate && c.oldCost > 0:
			c.source = rivian.CostSourceHomeRate
		case atHomeRate:
			continue // unpriced slow charge: leave for the read-path fallback
		default:
			c.source = rivian.CostSourceRivian
		}
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "reprice: rows:", err)
		return 1
	}
	_ = rows.Close()

	totals := map[string][2]float64{}
	repriced := 0
	for _, c := range changes {
		t := totals[c.user]
		t[0] += c.oldCost
		t[1] += c.newCost
		totals[c.user] = t
		if c.source == rivian.CostSourceNetworkEstimate {
			repriced++
			fmt.Printf("%s  %-10s %6.1f kWh  %5.0f kW  $%7.2f → $%7.2f  %s @ $%.2f/kWh\n",
				c.at.Format("2006-01-02 15:04"), c.user, c.kwh, c.kw, c.oldCost, c.newCost, c.network, c.rate)
		}
	}
	fmt.Printf("\n%d legacy rows classified, %d DC fast charges re-priced\n", len(changes), repriced)
	for u, t := range totals {
		fmt.Printf("  %s: $%.2f → $%.2f (%+.2f)\n", u, t[0], t[1], t[1]-t[0])
	}
	if !*apply {
		fmt.Println("\ndry run: nothing written (pass --apply to write)")
		return 0
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reprice: begin:", err)
		return 1
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range changes {
		var err error
		if c.source == rivian.CostSourceNetworkEstimate {
			_, err = tx.ExecContext(ctx, `
				UPDATE charges SET cost = $2, price_per_kwh = $3, currency = 'USD',
				       cost_source = $4, network = NULLIF($5, ''), updated_at = NOW()
				WHERE id = $1 AND cost_source IS NULL`,
				c.id, c.newCost, c.rate, c.source, c.network)
		} else {
			_, err = tx.ExecContext(ctx,
				`UPDATE charges SET cost_source = $2 WHERE id = $1 AND cost_source IS NULL`, c.id, c.source)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "reprice: update:", err)
			return 1
		}
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "reprice: commit:", err)
		return 1
	}
	fmt.Printf("applied %d updates\n", len(changes))
	return 0
}
