// Package blips is the repair for wake-blip charges: rows the recorder wrote when a truck woke
// from sleep, its first vehicleState frame replayed the pre-sleep "charging_active" and the next
// frame, about a second later, said complete (fixed in the recorder by liveCharge.blip, #1254). A
// blip is a live charge shorter than 20 s that moved the battery less than half a point.
//
// Unlike internal/repair/phantom this tool FINDS its candidates, by that one predicate, and an
// apply must present the count and the sha256 of the sorted id list of an approved dry run:
// anything else aborts the transaction. It deletes in one transaction, backs every row and its
// cascading dependents up (CSV and charges_deleted_backup, the phantom repair's format, so
// phantom.Restore and phantom.RestoreRows undo it), and refuses rows a user touched.
package blips

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/repair/phantom"
)

// The recorder's bounds (internal/rivian liveChargeBlipMax and liveChargeBlipSoC): the repair uses
// the same predicate on stored rows.
const (
	MaxDuration = 20 * time.Second
	MaxSoCDelta = 0.5
	// RecentOpenWindow: an open live charge touched within this long means a truck is charging
	// now; the repair does not run then (the recorder is writing, and it is not a quiet hour).
	RecentOpenWindow = 15 * time.Minute
)

// Row is a candidate: a live charge shorter than MaxDuration, with what the rule needs.
type Row struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	VehicleID  uuid.UUID
	StartedAt  time.Time
	EndedAt    time.Time
	StartSoC   *float64
	EndSoC     *float64
	FinalState string
	Source     string
	CostSource string
	// What a person or another table says about the row.
	HasRestamp bool
	HasReceipt bool
	Edited     bool // a place or location override, a note or a tag
	HasPack    bool // a pack-health sample points at it (cascades; backed up)
}

// Verdict is the rule's answer for one row.
type Verdict struct {
	Delete bool
	Reason string // why a row is skipped ("" when Delete)
}

// Classify applies the rule. User edits and anything open are refused before the physics test.
func Classify(r Row) Verdict {
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	switch {
	case r.Source != "live":
		return skip("not a live row")
	case charges.IsOpenLiveFinalState(r.FinalState):
		return skip("open: final_state " + r.FinalState)
	case strings.EqualFold(r.CostSource, "user"):
		return skip("carries a user-entered price")
	case r.HasRestamp:
		return skip("carries a place correction")
	case r.HasReceipt:
		return skip("a receipt points at it")
	case r.Edited:
		return skip("carries a user override, note or tag")
	case r.StartSoC == nil || r.EndSoC == nil:
		return skip("cannot verify: no SoC recorded")
	case r.EndedAt.Sub(r.StartedAt) >= MaxDuration:
		return skip("20 s or longer")
	}
	d := *r.EndSoC - *r.StartSoC
	if d < 0 {
		d = -d
	}
	if d >= MaxSoCDelta {
		return skip("moved the battery half a point or more")
	}
	return Verdict{Delete: true}
}

// Queryer is *sql.DB or *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Select loads the candidates (every live charge shorter than MaxDuration), classified later. With
// forUpdate the rows are locked, for the transaction that deletes them.
func Select(ctx context.Context, q Queryer, forUpdate bool) ([]Row, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF c"
	}
	rows, err := q.QueryContext(ctx, `
		SELECT c.id, c.user_id, c.vehicle_id, c.started_at, c.ended_at, c.start_soc_pct, c.end_soc_pct,
		       COALESCE(c.final_state,''), c.source, COALESCE(c.cost_source,''),
		       EXISTS (SELECT 1 FROM charge_restamps r WHERE r.charge_id = c.id),
		       EXISTS (SELECT 1 FROM rivian_receipts rr WHERE rr.charge_id = c.id),
		       (c.place_override IS NOT NULL OR c.location_override IS NOT NULL OR COALESCE(c.notes,'') <> '' OR cardinality(c.tags) > 0),
		       EXISTS (SELECT 1 FROM vehicle_pack_health_samples s WHERE s.charge_id = c.id)
		FROM charges c
		WHERE c.source = 'live' AND c.ended_at - c.started_at < interval '20 seconds'
		ORDER BY c.vehicle_id, c.started_at, c.id`+lock)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var s0, s1 sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.UserID, &r.VehicleID, &r.StartedAt, &r.EndedAt, &s0, &s1, &r.FinalState, &r.Source, &r.CostSource,
			&r.HasRestamp, &r.HasReceipt, &r.Edited, &r.HasPack); err != nil {
			return nil, err
		}
		if s0.Valid {
			v := s0.Float64
			r.StartSoC = &v
		}
		if s1.Valid {
			v := s1.Float64
			r.EndSoC = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Plan is the classified candidates.
type Plan struct {
	Delete  []Row
	Skipped map[string]int // reason -> count
}

// MakePlan classifies rows.
func MakePlan(rows []Row) Plan {
	p := Plan{Skipped: map[string]int{}}
	for _, r := range rows {
		if v := Classify(r); v.Delete {
			p.Delete = append(p.Delete, r)
		} else {
			p.Skipped[v.Reason]++
		}
	}
	return p
}

// IDs returns the ids to delete, sorted.
func (p Plan) IDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(p.Delete))
	for i, r := range p.Delete {
		ids[i] = r.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// Digest is the sha256 (hex) of the sorted ids, one per line: what a dry run prints and an apply
// must match.
func Digest(ids []uuid.UUID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = id.String()
	}
	sort.Strings(s)
	sum := sha256.Sum256([]byte(strings.Join(s, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// ByVehicle counts the rows to delete per vehicle id.
func (p Plan) ByVehicle() map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, r := range p.Delete {
		m[r.VehicleID]++
	}
	return m
}

// ByMonth counts the rows to delete per month of their start (UTC, "2006-01").
func (p Plan) ByMonth() map[string]int {
	m := map[string]int{}
	for _, r := range p.Delete {
		m[r.StartedAt.UTC().Format("2006-01")]++
	}
	return m
}

// OpenCharges counts live charges that are open and were written within RecentOpenWindow: a truck
// charging now.
func OpenCharges(ctx context.Context, q Queryer) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `
		SELECT count(*) FROM charges
		 WHERE source = 'live'
		   AND final_state IN ('charging_active', 'charging_ready', 'charging_connecting', 'waiting_on_charger')
		   AND (ended_at > now() - $1::interval OR updated_at > now() - $1::interval)`,
		fmt.Sprintf("%d seconds", int(RecentOpenWindow.Seconds()))).Scan(&n)
	return n, err
}

// ErrDrift: the rows no longer match the approved dry run (nothing was deleted).
var ErrDrift = errors.New("blips: the rows to delete differ from the approved dry run; nothing deleted")

// ErrOpenCharge: a truck is charging now; the repair does not run.
var ErrOpenCharge = errors.New("blips: a live charge was written in the last 15 minutes; run in a quiet hour")

// Apply deletes the blips in ONE transaction. It locks and re-selects the candidates, re-applies
// the rule, and aborts unless the count AND the digest of the sorted ids equal the approved dry
// run's. The backup rows (the charges and their cascading pack-health samples) go to sink (the
// durable CSV; an error aborts before anything is deleted) and to charges_deleted_backup when it
// exists. It aborts if the deleted count differs.
func Apply(ctx context.Context, d *sql.DB, batchID uuid.UUID, wantCount int, wantDigest, reason string, allowOpen bool, sink func([]phantom.BackupRow) error) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if !allowOpen {
		if n, err := OpenCharges(ctx, tx); err != nil {
			return 0, err
		} else if n > 0 {
			return 0, fmt.Errorf("%w (%d open)", ErrOpenCharge, n)
		}
	}
	rows, err := Select(ctx, tx, true)
	if err != nil {
		return 0, err
	}
	plan := MakePlan(rows)
	ids := plan.IDs()
	if len(ids) != wantCount || Digest(ids) != wantDigest {
		return 0, fmt.Errorf("%w: found %d (sha256 %s), approved %d (sha256 %s)", ErrDrift, len(ids), Digest(ids), wantCount, wantDigest)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	arr := make([]string, len(ids))
	for i, id := range ids {
		arr[i] = id.String()
	}
	idArr := "{" + strings.Join(arr, ",") + "}"

	var backup []phantom.BackupRow
	collect := func(kind, q string) error {
		rs, err := tx.QueryContext(ctx, q, idArr)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			b := phantom.BackupRow{Kind: kind}
			var raw []byte
			if err := rs.Scan(&b.ChargeID, &b.UserID, &raw); err != nil {
				return err
			}
			b.Row = json.RawMessage(raw)
			backup = append(backup, b)
		}
		return rs.Err()
	}
	if err := collect("charge", `SELECT c.id, c.user_id, to_jsonb(c.*) FROM charges c WHERE c.id = ANY($1::uuid[])`); err != nil {
		return 0, err
	}
	if err := collect("pack_health_sample", `SELECT s.charge_id, c.user_id, to_jsonb(s.*) FROM vehicle_pack_health_samples s JOIN charges c ON c.id = s.charge_id WHERE s.charge_id = ANY($1::uuid[])`); err != nil {
		return 0, err
	}
	if sink != nil {
		if err := sink(backup); err != nil {
			return 0, fmt.Errorf("backup sink: %w", err)
		}
	}
	if ok, err := phantom.BackupTableExists(ctx, d); err != nil {
		return 0, err
	} else if ok {
		for _, b := range backup {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row, reason)
				VALUES ($1,$2,$3,$4,$5::jsonb,$6)`, batchID, b.Kind, b.ChargeID, b.UserID, string(b.Row), reason); err != nil {
				return 0, err
			}
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM charges WHERE source = 'live' AND id = ANY($1::uuid[])`, idArr)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if int(n) != wantCount {
		return 0, fmt.Errorf("deleted %d of %d rows; rolling back", n, wantCount)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}
