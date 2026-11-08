// Package chargewatch measures open live charge rows: the guardrail behind "reliable charging data stays reliable" (#1670). A row stays open
// while a session is in progress; one that outlives its charge is what stretched a row across the drives that followed (#1619). The stale-charge
// janitor (internal/rivian sweepStaleCharges) should close a row an hour after its last write, so a not-charging open row that is much older
// is one it could not close. The figures are counts and ages, with no vehicle or user in them.
package chargewatch

import (
	"context"
	"database/sql"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// Summary is the open live charge rows now, split by what the vehicle's latest frame says. Age is measured from the row's last write
// (ended_at), which the recorder keeps moving while the car is plugged in and charging: the janitor acts an hour after it stops.
type Summary struct {
	Charging, NotCharging                   int
	OldestChargingSec, OldestNotChargingSec float64
}

// frameWindow bounds the latest-frame lookup so it prunes to the recent partitions; a row whose vehicle has sent nothing for longer than this
// counts as not charging.
const frameWindow = 2 * 24 * time.Hour

// Measure reads the open live rows (every user: pass the system pool) and, per vehicle that has one, the latest vehicle_state frame within
// frameWindow, judged with the janitor's own rule (rivian.FrameStillCharging).
func Measure(ctx context.Context, db *sql.DB, now time.Time) (Summary, error) {
	type open struct {
		vehicle string
		endedAt time.Time
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.vehicle_id::text, c.ended_at FROM charges c
		 WHERE c.source = 'live' AND c.final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`)
	if err != nil {
		return Summary{}, err
	}
	var all []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.vehicle, &o.endedAt); err != nil {
			rows.Close()
			return Summary{}, err
		}
		all = append(all, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}
	var sum Summary
	charging := map[string]bool{} // per vehicle: one latest-frame read, however many open rows it has
	for _, o := range all {
		still, seen := charging[o.vehicle]
		if !seen {
			var cs, status, gear sql.NullString
			var kw sql.NullFloat64
			err := db.QueryRowContext(ctx, `
				SELECT charging_state, charger_status, charger_power_kw, shift_state FROM vehicle_state
				 WHERE vehicle_id = $1::uuid AND at > $2 ORDER BY at DESC LIMIT 1`, o.vehicle, now.Add(-frameWindow)).Scan(&cs, &status, &kw, &gear)
			switch {
			case err == sql.ErrNoRows:
			case err != nil:
				return Summary{}, err
			default:
				still = rivian.FrameStillCharging(cs.String, status.String, kw.Float64, gear.String)
			}
			charging[o.vehicle] = still
		}
		age := now.Sub(o.endedAt).Seconds()
		if age < 0 {
			age = 0
		}
		if still {
			sum.Charging++
			if age > sum.OldestChargingSec {
				sum.OldestChargingSec = age
			}
		} else {
			sum.NotCharging++
			if age > sum.OldestNotChargingSec {
				sum.OldestNotChargingSec = age
			}
		}
	}
	return sum, nil
}

// Run measures every interval until ctx ends and reports each Summary. Every replica runs it: the read is cheap and idempotent, and a gauge that
// every pod exports does not disappear when one pod does (a singleton's series dropped out on each rollout, which flapped the Stripe cancel alert).
func Run(ctx context.Context, db *sql.DB, every time.Duration, clock func() time.Time, report func(Summary)) {
	if db == nil || report == nil {
		return
	}
	read := func() {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if s, err := Measure(rctx, db, clock()); err == nil {
			report(s)
		}
	}
	read()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			read()
		}
	}
}
