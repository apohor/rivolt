// Package phantom is the repair for phantom (stale-copy) live charges:
// rows the recorder fabricated from a replayed previous-session total,
// with no power and no SoC change behind them (architect's
// scratchpad/phantom-charges.md; devils-advocate's audit). It deletes
// only the ids it is given, re-checks the classification rule at run
// time, backs every row up (table + CSV) before deleting, deletes per
// vehicle in one transaction and can restore a batch.
//
// Nothing here is automatic: a human supplies the audited id list.
package phantom

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ReportOnlyKWh: rows under this energy are reported, never deleted
// (devils-advocate: low-value trickle cases are ambiguous).
const ReportOnlyKWh = 5.0

// Row is one charge with what the rule needs.
type Row struct {
	ID         uuid.UUID
	ExternalID string
	UserID     uuid.UUID
	VehicleID  uuid.UUID
	RivianID   string
	StartedAt  time.Time
	EndedAt    time.Time
	EnergyKWh  float64
	StartSoC   *float64
	EndSoC     *float64
	PackKWh    *float64
	MaxPowerKW *float64
	Cost       *float64
	Currency   string
	FinalState string
	CostSource string
	Source     string
	// PowerKWh is the integral of vehicle_state.charger_power_kw over
	// the row's window, counting only charging_active frames with each
	// gap capped at 10 min (stale non-zero power after pre-#51 charges
	// must not inflate it).
	PowerKWh float64
	// HasRestamp: a charge_restamps row (a user's place correction)
	// points at it. User edits are never deleted.
	HasRestamp bool
}

// Verdict is the rule's answer for one row.
type Verdict struct {
	Delete bool
	// Reason explains a skip ("" when Delete).
	Reason string
}

// Classify applies the rule. Order matters: user edits and out-of-scope
// rows are refused before the physics test.
func Classify(r Row) Verdict {
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	switch {
	case r.Source != "live":
		return skip("not a live row")
	case strings.EqualFold(r.CostSource, "user"):
		return skip("carries a user-entered price")
	case r.HasRestamp:
		return skip("carries a place correction")
	case r.EnergyKWh < ReportOnlyKWh:
		return skip(fmt.Sprintf("report only: under %.0f kWh", ReportOnlyKWh))
	case r.StartSoC == nil || r.EndSoC == nil:
		return skip("cannot verify: no SoC recorded")
	}
	delta := *r.EndSoC - *r.StartSoC
	if r.PackKWh != nil && *r.PackKWh > 0 {
		if delta*(*r.PackKWh)/100 >= 0.25*r.EnergyKWh {
			return skip("the SoC change accounts for the energy")
		}
	} else if delta >= 0.5 {
		return skip("the SoC change accounts for the energy (no pack size known)")
	}
	if r.PowerKWh >= 0.25*r.EnergyKWh {
		return skip(fmt.Sprintf("power delivered %.1f kWh of the %.1f (preconditioning or a real charge)", r.PowerKWh, r.EnergyKWh))
	}
	return Verdict{Delete: true}
}

// Queryer is satisfied by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Load reads the requested ids for one user, in id order not
// guaranteed, with the power integral and restamp flag. Ids not found
// (or another user's) are simply absent: the caller reports them.
func Load(ctx context.Context, q Queryer, userID uuid.UUID, ids []uuid.UUID) ([]Row, error) {
	if userID == uuid.Nil || len(ids) == 0 {
		return nil, nil
	}
	arr := make([]string, len(ids))
	for i, id := range ids {
		arr[i] = id.String()
	}
	rows, err := q.QueryContext(ctx, `
		SELECT c.id, c.external_id, c.user_id, c.vehicle_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at, COALESCE(c.energy_added_kwh, 0)::float8,
		       c.start_soc_pct, c.end_soc_pct, v.pack_kwh, c.max_power_kw,
		       c.cost::float8, COALESCE(c.currency, ''), COALESCE(c.final_state, ''),
		       COALESCE(c.cost_source, ''), c.source,
		       COALESCE((
		           SELECT SUM(x.p) FROM (
		               SELECT vs.charging_state,
		                      vs.charger_power_kw * LEAST(EXTRACT(EPOCH FROM (LEAD(vs.at) OVER w - vs.at)), 600) / 3600.0 AS p
		                 FROM vehicle_state vs
		                WHERE vs.user_id = c.user_id AND vs.vehicle_id = c.vehicle_id
		                  AND vs.at >= c.started_at AND vs.at <= c.ended_at
		               WINDOW w AS (ORDER BY vs.at)
		           ) x
		           WHERE x.charging_state = 'charging_active' AND x.p IS NOT NULL
		       ), 0)::float8,
		       EXISTS (SELECT 1 FROM charge_restamps r WHERE r.user_id = c.user_id AND r.charge_id = c.id)
		  FROM charges c
		  JOIN vehicles v ON v.id = c.vehicle_id
		 WHERE c.user_id = $1 AND c.id = ANY($2::uuid[])
		 ORDER BY c.vehicle_id, c.started_at`,
		userID, "{"+strings.Join(arr, ",")+"}")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var s0, s1, pack, maxp, cost sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.ExternalID, &r.UserID, &r.VehicleID, &r.RivianID,
			&r.StartedAt, &r.EndedAt, &r.EnergyKWh, &s0, &s1, &pack, &maxp, &cost, &r.Currency,
			&r.FinalState, &r.CostSource, &r.Source, &r.PowerKWh, &r.HasRestamp); err != nil {
			return nil, err
		}
		r.StartedAt, r.EndedAt = r.StartedAt.UTC(), r.EndedAt.UTC()
		r.StartSoC, r.EndSoC, r.PackKWh, r.MaxPowerKW, r.Cost = nfp(s0), nfp(s1), nfp(pack), nfp(maxp), nfp(cost)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nfp(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// MonthTotals is the counted energy and cost of the rows to delete, per
// month (for the dry-run summary: "counted vs resulting").
type MonthTotals struct {
	Month string
	Rows  int
	KWh   float64
	Cost  float64
}

// Summarise groups the deletable rows by UTC month.
func Summarise(rows []Row, verdicts map[uuid.UUID]Verdict) []MonthTotals {
	idx := map[string]*MonthTotals{}
	var order []string
	for _, r := range rows {
		if !verdicts[r.ID].Delete {
			continue
		}
		m := r.StartedAt.Format("2006-01")
		t, ok := idx[m]
		if !ok {
			t = &MonthTotals{Month: m}
			idx[m] = t
			order = append(order, m)
		}
		t.Rows++
		t.KWh += r.EnergyKWh
		if r.Cost != nil {
			t.Cost += *r.Cost
		}
	}
	out := make([]MonthTotals, 0, len(order))
	for _, m := range order {
		out = append(out, *idx[m])
	}
	return out
}

// Result of applying one vehicle's deletions.
type Result struct {
	VehicleID uuid.UUID
	Deleted   int
	Err       error
}

// ErrDrift is returned when a row no longer classifies as a phantom at
// apply time (it changed since the dry run).
var ErrDrift = errors.New("phantom: a row changed since the dry run; nothing deleted for this vehicle")

// Apply deletes the given rows of ONE vehicle in a single transaction:
// it re-loads and re-classifies them inside the transaction (any row
// that no longer qualifies aborts the vehicle), copies every row and
// every cascading dependent into charges_deleted_backup under batchID,
// deletes, and aborts unless the counts match.
func Apply(ctx context.Context, d *sql.DB, batchID, userID, vehicleID uuid.UUID, ids []uuid.UUID, reason string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	fresh, err := Load(ctx, tx, userID, ids)
	if err != nil {
		return 0, err
	}
	if len(fresh) != len(ids) {
		return 0, fmt.Errorf("%w (found %d of %d)", ErrDrift, len(fresh), len(ids))
	}
	for _, r := range fresh {
		if r.VehicleID != vehicleID {
			return 0, fmt.Errorf("row %s belongs to another vehicle", r.ExternalID)
		}
		if v := Classify(r); !v.Delete {
			return 0, fmt.Errorf("%w: %s: %s", ErrDrift, r.ExternalID, v.Reason)
		}
	}
	arr := make([]string, len(ids))
	for i, id := range ids {
		arr[i] = id.String()
	}
	idArr := "{" + strings.Join(arr, ",") + "}"

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row, reason)
		SELECT $1, 'charge', c.id, c.user_id, to_jsonb(c.*), $4
		  FROM charges c WHERE c.user_id = $2 AND c.id = ANY($3::uuid[])`,
		batchID, userID, idArr, reason); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row, reason)
		SELECT $1, 'pack_health_sample', s.charge_id, $2, to_jsonb(s.*), $4
		  FROM vehicle_pack_health_samples s
		 WHERE s.vehicle_id = $5 AND s.charge_id = ANY($3::uuid[])`,
		batchID, userID, idArr, reason, vehicleID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row, reason)
		SELECT $1, 'charge_restamp', r.charge_id, r.user_id, to_jsonb(r.*), $4
		  FROM charge_restamps r WHERE r.user_id = $2 AND r.charge_id = ANY($3::uuid[])`,
		batchID, userID, idArr, reason); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		DELETE FROM charges WHERE user_id = $1 AND source = 'live' AND id = ANY($2::uuid[])`, userID, idArr)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if int(n) != len(ids) {
		return 0, fmt.Errorf("deleted %d of %d rows; rolling back", n, len(ids))
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// RestoreResult is what a restore put back.
type RestoreResult struct {
	Charges, PackHealth, Restamps int
}

// Restore re-inserts a batch from charges_deleted_backup in one
// transaction. It refuses a batch that was already restored and any
// charge id that exists again (nothing is partially restored).
func Restore(ctx context.Context, d *sql.DB, batchID uuid.UUID) (RestoreResult, error) {
	var out RestoreResult
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	var total, restored int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), count(restored_at) FROM charges_deleted_backup WHERE batch_id = $1`, batchID).Scan(&total, &restored); err != nil {
		return out, err
	}
	if total == 0 {
		return out, fmt.Errorf("batch %s not found", batchID)
	}
	if restored > 0 {
		return out, fmt.Errorf("batch %s was already restored", batchID)
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM charges c
		  JOIN charges_deleted_backup b ON b.charge_id = c.id AND b.kind = 'charge'
		 WHERE b.batch_id = $1`, batchID).Scan(&exists); err != nil {
		return out, err
	}
	if exists > 0 {
		return out, fmt.Errorf("%d charge id(s) of batch %s exist again; refusing a partial restore", exists, batchID)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO charges
		SELECT (jsonb_populate_record(NULL::charges, b.row)).*
		  FROM charges_deleted_backup b WHERE b.batch_id = $1 AND b.kind = 'charge'`, batchID)
	if err != nil {
		return out, err
	}
	n, _ := res.RowsAffected()
	out.Charges = int(n)
	res, err = tx.ExecContext(ctx, `
		INSERT INTO vehicle_pack_health_samples
		SELECT (jsonb_populate_record(NULL::vehicle_pack_health_samples, b.row)).*
		  FROM charges_deleted_backup b WHERE b.batch_id = $1 AND b.kind = 'pack_health_sample'
		ON CONFLICT DO NOTHING`, batchID)
	if err != nil {
		return out, err
	}
	n, _ = res.RowsAffected()
	out.PackHealth = int(n)
	res, err = tx.ExecContext(ctx, `
		INSERT INTO charge_restamps
		SELECT (jsonb_populate_record(NULL::charge_restamps, b.row)).*
		  FROM charges_deleted_backup b WHERE b.batch_id = $1 AND b.kind = 'charge_restamp'
		ON CONFLICT DO NOTHING`, batchID)
	if err != nil {
		return out, err
	}
	n, _ = res.RowsAffected()
	out.Restamps = int(n)
	if _, err := tx.ExecContext(ctx, `UPDATE charges_deleted_backup SET restored_at = NOW() WHERE batch_id = $1`, batchID); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
