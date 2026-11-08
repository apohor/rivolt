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
// (ended_at), which the recorder keeps moving while the car is plugged in and charging: the janitor acts an hour after it stops. A row whose
// vehicle has sent no frame at all within frameWindow is counted apart (NoFrame): a dormant account's leftover row is not a stuck charge, and
// is what the --close-stale-open repair (#1641) is for, so it must not trip the not-charging rule.
type Summary struct {
	Charging, NotCharging, NoFrame                            int
	OldestChargingSec, OldestNotChargingSec, OldestNoFrameSec float64
}

// frameWindow bounds the latest-frame lookup so it prunes to the recent partitions; a row whose vehicle has sent nothing for longer than this
// is counted as no_frame.
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
	const (
		stCharging = iota + 1
		stNot
		stNoFrame
	)
	verdict := map[string]int{} // per vehicle: one latest-frame read, however many open rows it has
	for _, o := range all {
		v, seen := verdict[o.vehicle]
		if !seen {
			var cs, status, gear sql.NullString
			var kw sql.NullFloat64
			err := db.QueryRowContext(ctx, `
				SELECT charging_state, charger_status, charger_power_kw, shift_state FROM vehicle_state
				 WHERE vehicle_id = $1::uuid AND at > $2 ORDER BY at DESC LIMIT 1`, o.vehicle, now.Add(-frameWindow)).Scan(&cs, &status, &kw, &gear)
			switch {
			case err == sql.ErrNoRows:
				v = stNoFrame
			case err != nil:
				return Summary{}, err
			case rivian.FrameStillCharging(cs.String, status.String, kw.Float64, gear.String):
				v = stCharging
			default:
				v = stNot
			}
			verdict[o.vehicle] = v
		}
		age := now.Sub(o.endedAt).Seconds()
		if age < 0 {
			age = 0
		}
		switch v {
		case stCharging:
			sum.Charging++
			sum.OldestChargingSec = max(sum.OldestChargingSec, age)
		case stNoFrame:
			sum.NoFrame++
			sum.OldestNoFrameSec = max(sum.OldestNoFrameSec, age)
		default:
			sum.NotCharging++
			sum.OldestNotChargingSec = max(sum.OldestNotChargingSec, age)
		}
	}
	return sum, nil
}

// Run measures every interval until ctx ends and reports each good Summary (a failed read goes to onError and reports nothing). Every replica runs it: the read is cheap and idempotent, and a gauge that
// every pod exports does not disappear when one pod does (a singleton's series dropped out on each rollout, which flapped the Stripe cancel alert).
func Run(ctx context.Context, db *sql.DB, every time.Duration, clock func() time.Time, report func(Summary), onError func(error)) {
	if db == nil || report == nil {
		return
	}
	read := func() {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s, err := Measure(rctx, db, clock())
		if err != nil {
			// The gauges keep their last values on an error, which would read as "all clear": the caller logs it and the
			// last-success timestamp stops moving, which the infra alert watches.
			if onError != nil {
				onError(err)
			}
			return
		}
		report(s)
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
