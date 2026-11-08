// Package stretched is the repair for charge rows the stale-charge janitor stretched past their real end (#1619,
// #1630): it refreshed an open row's ended_at on a charger state that stuck after an unplug, so the row ran on
// through the vehicle's later drives. The repair caps ended_at at the last frame the vehicle read plugged in.
//
// Like internal/repair/blips it FINDS its candidates by one predicate, and an apply must present the count and
// the sha256 of the sorted id list of an approved dry run; anything else aborts the transaction. It changes
// ended_at only, never later and never before started_at, in one transaction, after the old row (to_jsonb) went
// to charges_capped_backup and the durable CSV sink. It refuses open rows, rows written in the last hour and rows a user
// touched. A row whose plugged frames continue after a drive may hold a second, real charge; those are FLAGGED
// (reported, not changed): the rule is Flagger, and can be swapped.
package stretched

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
	"github.com/apohor/rivolt/internal/repair/blips"
	"github.com/apohor/rivolt/internal/repair/phantom"
)

const (
	// MinStretch: a row is stretched when ended_at is this far past the last plugged frame.
	MinStretch = time.Hour
	// QuietFor: a row written more recently than this is left alone (the recorder or the janitor may still be on it).
	QuietFor = time.Hour
)

// Row is a candidate: a live charge with at least one same-vehicle drive starting inside it, and the evidence.
type Row struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	VehicleID  uuid.UUID
	StartedAt  time.Time
	EndedAt    time.Time
	UpdatedAt  time.Time
	FinalState string
	Source     string
	CostSource string
	// FirstDrive is the start of the first drive inside the row.
	FirstDrive time.Time
	// LastPlugged is the last frame inside the row where the vehicle read plugged in (zero: none).
	LastPlugged time.Time
	// PluggedAfterDrive: a plugged frame exists after FirstDrive.
	PluggedAfterDrive bool
	// What a person or another table says about the row.
	HasRestamp bool
	HasReceipt bool
	Edited     bool
}

// Verdict is the rule's answer for one row.
type Verdict struct {
	CapTo  time.Time // set when the row is capped
	Reason string    // why a row is skipped or flagged ("" when capped)
	Flag   bool      // skipped because it may hold an absorbed charge
}

// Flagger decides whether a row may hold a second, real charge that the stretch swallowed, returning the
// reason ("" for no). The default looks at plugged frames after a drive; the rule is meant to be replaced by
// something that can also see a gradual SoC rise (DefaultFlagger is only the conservative start).
type Flagger func(Row) string

// DefaultFlagger flags a row with plugged frames after its first drive.
func DefaultFlagger(r Row) string {
	if r.PluggedAfterDrive {
		return "flagged: plugged frames after a drive (possible absorbed charge)"
	}
	return ""
}

// Classify applies the rule at time now.
func Classify(r Row, now time.Time, flag Flagger) Verdict {
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	switch {
	case r.Source != "live":
		return skip("not a live row")
	case charges.IsOpenLiveFinalState(r.FinalState):
		return skip("open: final_state " + r.FinalState)
	case now.Sub(r.UpdatedAt) < QuietFor:
		return skip("written in the last hour")
	case strings.EqualFold(r.CostSource, "user"):
		return skip("carries a user-entered price")
	case r.HasRestamp:
		return skip("carries a place correction")
	case r.HasReceipt:
		return skip("a receipt points at it")
	case r.Edited:
		return skip("carries a user override, note or tag")
	case r.FirstDrive.IsZero():
		return skip("no drive inside")
	case r.LastPlugged.IsZero():
		return skip("no plugged frame inside")
	}
	if flag != nil {
		if why := flag(r); why != "" {
			return Verdict{Reason: why, Flag: true}
		}
	}
	switch {
	case r.EndedAt.Sub(r.LastPlugged) <= MinStretch:
		return skip("not stretched")
	case r.LastPlugged.Before(r.StartedAt) || !r.LastPlugged.Before(r.EndedAt):
		return skip("cap outside the row")
	}
	return Verdict{CapTo: r.LastPlugged}
}

// Queryer is *sql.DB or *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// pluggedFrame is chargerPlugged for a stored vehicle_state frame, judged as if no charge were open (the
// recorder's rule in internal/rivian, minus the open-charge allowance): a connected status, or charging with
// power behind it.
const pluggedFrame = `(s.charger_status LIKE 'chrgr_sts_connected%'
    OR (s.charging_state IN ('charging_active','charging_connecting') AND COALESCE(s.charger_power_kw,0) > 0))`

// Select loads every live charge with a same-vehicle drive starting strictly inside it, with the evidence.
// With forUpdate the charge rows are locked, for the apply transaction.
func Select(ctx context.Context, q Queryer, forUpdate bool) ([]Row, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF c"
	}
	rows, err := q.QueryContext(ctx, `
		WITH cand AS (
		  SELECT c.id, c.user_id, c.vehicle_id, c.started_at, c.ended_at, c.updated_at,
		         COALESCE(c.final_state,'') AS final_state, c.source, COALESCE(c.cost_source,'') AS cost_source,
		         (SELECT min(d.started_at) FROM drives d
		           WHERE d.vehicle_id = c.vehicle_id AND d.started_at > c.started_at AND d.started_at < c.ended_at) AS first_drive,
		         EXISTS (SELECT 1 FROM charge_restamps r WHERE r.charge_id = c.id) AS restamp,
		         EXISTS (SELECT 1 FROM rivian_receipts rr WHERE rr.charge_id = c.id) AS receipt,
		         (c.place_override IS NOT NULL OR c.location_override IS NOT NULL OR COALESCE(c.notes,'') <> '' OR cardinality(c.tags) > 0) AS edited
		    FROM charges c
		   WHERE c.source = 'live'
		   ORDER BY c.vehicle_id, c.started_at, c.id`+lock+`
		)
		SELECT cand.id, cand.user_id, cand.vehicle_id, cand.started_at, cand.ended_at, cand.updated_at,
		       cand.final_state, cand.source, cand.cost_source, cand.first_drive,
		       (SELECT max(s.at) FROM vehicle_state s
		         WHERE s.vehicle_id = cand.vehicle_id AND s.at BETWEEN cand.started_at AND cand.ended_at AND `+pluggedFrame+`),
		       EXISTS (SELECT 1 FROM vehicle_state s
		         WHERE s.vehicle_id = cand.vehicle_id AND s.at > cand.first_drive AND s.at <= cand.ended_at AND `+pluggedFrame+`),
		       cand.restamp, cand.receipt, cand.edited
		  FROM cand WHERE cand.first_drive IS NOT NULL
		 ORDER BY cand.vehicle_id, cand.started_at, cand.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var drive, plugged sql.NullTime
		if err := rows.Scan(&r.ID, &r.UserID, &r.VehicleID, &r.StartedAt, &r.EndedAt, &r.UpdatedAt, &r.FinalState, &r.Source, &r.CostSource,
			&drive, &plugged, &r.PluggedAfterDrive, &r.HasRestamp, &r.HasReceipt, &r.Edited); err != nil {
			return nil, err
		}
		r.FirstDrive, r.LastPlugged = drive.Time, plugged.Time
		out = append(out, r)
	}
	return out, rows.Err()
}

// Plan is the classified candidates.
type Plan struct {
	Cap     []Row
	CapTo   map[uuid.UUID]time.Time
	Skipped map[string]int // reason -> count (flagged reasons included)
	Flagged []Row
}

// MakePlan classifies rows at time now. A nil flag means DefaultFlagger.
func MakePlan(rows []Row, now time.Time, flag Flagger) Plan {
	if flag == nil {
		flag = DefaultFlagger
	}
	p := Plan{CapTo: map[uuid.UUID]time.Time{}, Skipped: map[string]int{}}
	for _, r := range rows {
		v := Classify(r, now, flag)
		switch {
		case !v.CapTo.IsZero():
			p.Cap = append(p.Cap, r)
			p.CapTo[r.ID] = v.CapTo
		case v.Flag:
			p.Flagged = append(p.Flagged, r)
			p.Skipped[v.Reason]++
		default:
			p.Skipped[v.Reason]++
		}
	}
	return p
}

// IDs returns the ids to cap, sorted.
func (p Plan) IDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(p.Cap))
	for i, r := range p.Cap {
		ids[i] = r.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// FlaggedIDs returns the flagged ids, sorted.
func (p Plan) FlaggedIDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(p.Flagged))
	for i, r := range p.Flagged {
		ids[i] = r.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// Digest is the sha256 of the capped ids AND their new ended_at, so a changed cap point drifts the digest.
func (p Plan) Digest() string {
	ids := p.IDs()
	lines := make([]string, len(ids))
	for i, id := range ids {
		lines[i] = id.String() + " " + p.CapTo[id].UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// ByVehicle counts the rows to cap per vehicle id.
func (p Plan) ByVehicle() map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, r := range p.Cap {
		m[r.VehicleID]++
	}
	return m
}

// HoursRemoved is the total time the cap takes off the rows.
func (p Plan) HoursRemoved() float64 {
	var h float64
	for _, r := range p.Cap {
		h += r.EndedAt.Sub(p.CapTo[r.ID]).Hours()
	}
	return h
}

// Repriceable counts capped rows whose price came from the home rate or a network estimate: their cost may
// change with the window (time of use). The repair does not re-price; `rivolt reprice --ids` does, on the list
// the dry run writes.
func (p Plan) Repriceable() int {
	n := 0
	for _, r := range p.Cap {
		if r.CostSource == "home_rate" || r.CostSource == "network_estimate" {
			n++
		}
	}
	return n
}

// ErrDrift: the rows no longer match the approved dry run (nothing was changed).
var ErrDrift = errors.New("stretched: the rows to cap differ from the approved dry run; nothing changed")

// ErrOpenCharge: a truck is charging now; the repair does not run.
var ErrOpenCharge = errors.New("stretched: a live charge was written in the last 15 minutes; run in a quiet hour")

// Apply caps ended_at in ONE transaction. It locks and re-selects the candidates, re-applies the rule at the
// current time, and aborts unless the count AND the digest equal the approved dry run's. The old rows (to_jsonb)
// go to sink (the durable CSV; an error aborts before anything is changed) and to charges_capped_backup. Each
// update asserts the cap is earlier than the old ended_at and not before started_at, and touches exactly one row.
func Apply(ctx context.Context, d *sql.DB, batchID uuid.UUID, wantCount int, wantDigest, reason string, allowOpen bool, flag Flagger, sink func([]phantom.BackupRow) error) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if !allowOpen {
		if n, err := blips.OpenCharges(ctx, tx); err != nil {
			return 0, err
		} else if n > 0 {
			return 0, fmt.Errorf("%w (%d open)", ErrOpenCharge, n)
		}
	}
	rows, err := Select(ctx, tx, true)
	if err != nil {
		return 0, err
	}
	plan := MakePlan(rows, time.Now(), flag)
	ids := plan.IDs()
	if len(ids) != wantCount || plan.Digest() != wantDigest {
		return 0, fmt.Errorf("%w: found %d (sha256 %s), approved %d (sha256 %s)", ErrDrift, len(ids), plan.Digest(), wantCount, wantDigest)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	var backup []phantom.BackupRow
	for _, r := range plan.Cap {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(c.*) FROM charges c WHERE c.id = $1`, r.ID).Scan(&raw); err != nil {
			return 0, err
		}
		backup = append(backup, phantom.BackupRow{Kind: "charge_cap", ChargeID: r.ID, UserID: r.UserID, Row: json.RawMessage(raw)})
	}
	if sink != nil {
		if err := sink(backup); err != nil {
			return 0, fmt.Errorf("backup sink: %w", err)
		}
	}
	for _, b := range backup {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO charges_capped_backup (batch_id, charge_id, user_id, row, capped_to, reason)
			VALUES ($1,$2,$3,$4::jsonb,$5,$6)`, batchID, b.ChargeID, b.UserID, string(b.Row), plan.CapTo[b.ChargeID], reason); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, r := range plan.Cap {
		to := plan.CapTo[r.ID]
		// The assertions are in the statement: never later than the old end, never before the start.
		res, err := tx.ExecContext(ctx, `
			UPDATE charges SET ended_at = $2, updated_at = now()
			 WHERE id = $1 AND source = 'live' AND ended_at = $3 AND $2 < ended_at AND $2 >= started_at`, r.ID, to, r.EndedAt)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return 0, fmt.Errorf("stretched: row %s changed %d rows; rolling back", r.ID, k)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// Restore puts a batch's rows back from charges_capped_backup: ended_at as it was (updated_at is touched by the
// table's trigger on every update, so it reads the restore time; the old value stays in the backup row). A row whose
// ended_at is no longer the capped value (it changed since) is left alone and counted in skipped. A restored
// batch is refused.
func Restore(ctx context.Context, d *sql.DB, batchID uuid.UUID) (restored, skipped int, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM charges_capped_backup WHERE batch_id = $1 AND restored_at IS NULL`, batchID).Scan(&open); err != nil {
		return 0, 0, err
	}
	if open == 0 {
		return 0, 0, fmt.Errorf("stretched: batch %s has nothing to restore (unknown or already restored)", batchID)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE charges c
		   SET ended_at = (b.row->>'ended_at')::timestamptz
		  FROM charges_capped_backup b
		 WHERE b.batch_id = $1 AND b.restored_at IS NULL AND c.id = b.charge_id AND c.ended_at = b.capped_to`, batchID)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `UPDATE charges_capped_backup SET restored_at = now() WHERE batch_id = $1 AND restored_at IS NULL`, batchID); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int(n), open - int(n), nil
}
