// Package closestale is the repair for open-state live charge rows the stale-charge janitor can never reach (#1641): the janitor
// sweeps a user's rows only while that user's monitor runs, so a row left open by an account that has gone dormant (disconnected,
// vehicle no longer reporting) stays open for good. It does not show as in progress (its ended_at is old), but the ledger counts
// any open final_state ("N sessions are still charging; totals will change"). The repair closes it: final_state by the SoC rule
// (charging_complete if the SoC rose over the row, else abandoned); ended_at and everything else are untouched.
//
// Same gate as the other repairs: the dry run finds its candidates by one predicate; an apply presents the count and the sha256 of
// the approved dry run and runs in one transaction under FOR UPDATE; user-touched rows are refused; the old row goes to
// charges_capped_backup (and a CSV); the undo is all or nothing and puts back only what the repair changed.
package closestale

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

	"github.com/apohor/rivolt/internal/repair/blips"
	"github.com/apohor/rivolt/internal/repair/phantom"
)

const (
	// OlderThan: a row is a candidate when its last write is older than this. The janitor closes anything older than an hour
	// whose user is being monitored, so a day is long past its reach.
	OlderThan = 24 * time.Hour
	// QuietFor: the vehicle must have sent no frame for this long; a reporting vehicle's rows belong to the janitor.
	QuietFor = 24 * time.Hour
	// MarkerKey is the metadata key the repair sets on the rows it closed.
	MarkerKey = "stale_open_closed"
)

// Row is a candidate: an open-state live charge with the evidence the rule needs.
type Row struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	VehicleID  uuid.UUID
	StartedAt  time.Time
	EndedAt    time.Time
	UpdatedAt  time.Time
	FinalState string
	StartSoC   *float64
	EndSoC     *float64
	// LastFrameAt is the vehicle's newest vehicle_state frame within QuietFor of now (zero: none in that window). The lookup is
	// bounded so the partitioned table is pruned to the last day's partitions.
	LastFrameAt time.Time
	// What a person or another table says about the row.
	CostSource string
	HasRestamp bool
	HasReceipt bool
	Edited     bool // a place or location override, a note or a tag
}

// Verdict is the rule's answer for one row.
type Verdict struct {
	NewState string // the final_state to write ("" when skipped)
	Reason   string // why the row is skipped
}

// IsOpenState reports whether s is a final_state that means a session still in progress.
func IsOpenState(s string) bool {
	switch s {
	case "charging_active", "charging_ready", "charging_connecting", "waiting_on_charger":
		return true
	}
	return false
}

// Classify applies the rule at time now.
func Classify(r Row, now time.Time) Verdict {
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	switch {
	case !IsOpenState(r.FinalState):
		return skip("not open")
	case now.Sub(r.EndedAt) < OlderThan || now.Sub(r.UpdatedAt) < OlderThan:
		return skip("written in the last day")
	case !r.LastFrameAt.IsZero() && now.Sub(r.LastFrameAt) < QuietFor:
		return skip("the vehicle is reporting: the janitor owns the row")
	case strings.EqualFold(r.CostSource, "user"):
		return skip("carries a user-entered price")
	case r.HasRestamp:
		return skip("carries a place correction")
	case r.HasReceipt:
		return skip("a receipt points at it")
	case r.Edited:
		return skip("carries a user override, note or tag")
	}
	if r.StartSoC != nil && r.EndSoC != nil && *r.EndSoC > *r.StartSoC {
		return Verdict{NewState: "charging_complete"}
	}
	return Verdict{NewState: "abandoned"}
}

// Queryer is *sql.DB or *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Select loads the open-state live rows older than OlderThan; with forUpdate the rows are locked, for the apply transaction.
func Select(ctx context.Context, q Queryer, forUpdate bool) ([]Row, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF c"
	}
	rows, err := q.QueryContext(ctx, `
		SELECT c.id, c.user_id, c.vehicle_id, c.started_at, c.ended_at, c.updated_at, COALESCE(c.final_state,''),
		       c.start_soc_pct, c.end_soc_pct, COALESCE(c.cost_source,''),
		       (SELECT max(s.at) FROM vehicle_state s WHERE s.vehicle_id = c.vehicle_id AND s.at > now() - $2::interval),
		       EXISTS (SELECT 1 FROM charge_restamps r WHERE r.charge_id = c.id),
		       EXISTS (SELECT 1 FROM rivian_receipts rr WHERE rr.charge_id = c.id),
		       (c.place_override IS NOT NULL OR c.location_override IS NOT NULL OR COALESCE(c.notes,'') <> '' OR cardinality(c.tags) > 0)
		  FROM charges c
		 WHERE c.source = 'live'
		   AND c.final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')
		   AND c.ended_at < now() - $1::interval
		 ORDER BY c.vehicle_id, c.started_at, c.id`+lock, fmt.Sprintf("%d seconds", int(OlderThan.Seconds())), fmt.Sprintf("%d seconds", int(QuietFor.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var s0, s1 sql.NullFloat64
		var last sql.NullTime
		if err := rows.Scan(&r.ID, &r.UserID, &r.VehicleID, &r.StartedAt, &r.EndedAt, &r.UpdatedAt, &r.FinalState, &s0, &s1, &r.CostSource,
			&last, &r.HasRestamp, &r.HasReceipt, &r.Edited); err != nil {
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
		r.LastFrameAt = last.Time
		out = append(out, r)
	}
	return out, rows.Err()
}

// Plan is the classified candidates.
type Plan struct {
	Close    []Row
	NewState map[uuid.UUID]string
	Skipped  map[string]int
}

// MakePlan classifies rows at time now.
func MakePlan(rows []Row, now time.Time) Plan {
	p := Plan{NewState: map[uuid.UUID]string{}, Skipped: map[string]int{}}
	for _, r := range rows {
		v := Classify(r, now)
		if v.NewState == "" {
			p.Skipped[v.Reason]++
			continue
		}
		p.Close = append(p.Close, r)
		p.NewState[r.ID] = v.NewState
	}
	return p
}

// IDs returns the ids to close, sorted.
func (p Plan) IDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(p.Close))
	for i, r := range p.Close {
		ids[i] = r.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// Digest is the sha256 of the sorted ids with the final_state each gets and the row's ended_at, so a different decision or a
// row that moved changes it.
func (p Plan) Digest() string {
	byID := map[uuid.UUID]Row{}
	for _, r := range p.Close {
		byID[r.ID] = r
	}
	ids := p.IDs()
	lines := make([]string, len(ids))
	for i, id := range ids {
		lines[i] = fmt.Sprintf("%s %s %s", id, p.NewState[id], byID[id].EndedAt.UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// ByState counts the rows by the final_state they would get.
func (p Plan) ByState() map[string]int {
	m := map[string]int{}
	for _, s := range p.NewState {
		m[s]++
	}
	return m
}

// ByVehicle counts the rows to close per vehicle id.
func (p Plan) ByVehicle() map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, r := range p.Close {
		m[r.VehicleID]++
	}
	return m
}

// ErrDrift: the rows no longer match the approved dry run (nothing was changed).
var ErrDrift = errors.New("closestale: the rows to close differ from the approved dry run; nothing changed")

// ErrOpenCharge: a truck is charging now; the repair does not run.
var ErrOpenCharge = errors.New("closestale: a live charge was written in the last 15 minutes; run in a quiet hour")

// Apply closes the rows in ONE transaction: it locks and re-selects the candidates, re-applies the rule at the current time, and
// aborts unless the count AND the digest equal the approved dry run's. The old rows (to_jsonb) go to sink (the durable CSV; an
// error aborts before anything is changed) and to charges_capped_backup (capped_to = the row's unchanged ended_at, created_ids
// empty). Each update asserts the row is still open and unmoved, and touches exactly one row.
func Apply(ctx context.Context, d *sql.DB, batchID uuid.UUID, wantCount int, wantDigest, reason string, allowOpen bool, sink func([]phantom.BackupRow) error) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if !allowOpen {
		// Only rows younger than the candidates' day count: a truck charging now writes within minutes.
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
	plan := MakePlan(rows, time.Now())
	ids := plan.IDs()
	if len(ids) != wantCount || plan.Digest() != wantDigest {
		return 0, fmt.Errorf("%w: found %d (sha256 %s), approved %d (sha256 %s)", ErrDrift, len(ids), plan.Digest(), wantCount, wantDigest)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var backup []phantom.BackupRow
	for _, r := range plan.Close {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(c.*) FROM charges c WHERE c.id = $1`, r.ID).Scan(&raw); err != nil {
			return 0, err
		}
		backup = append(backup, phantom.BackupRow{Kind: "charge_close_stale", ChargeID: r.ID, UserID: r.UserID, Row: json.RawMessage(raw)})
	}
	if sink != nil {
		if err := sink(backup); err != nil {
			return 0, fmt.Errorf("backup sink: %w", err)
		}
	}
	for _, r := range plan.Close {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO charges_capped_backup (batch_id, charge_id, user_id, row, capped_to, reason)
			VALUES ($1,$2,$3,(SELECT to_jsonb(c.*) FROM charges c WHERE c.id = $2),$4,$5)`, batchID, r.ID, r.UserID, r.EndedAt, reason); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, r := range plan.Close {
		m := fmt.Sprintf(`{%q:{"batch":%q,"was":%q}}`, MarkerKey, batchID.String(), r.FinalState)
		res, err := tx.ExecContext(ctx, `
			UPDATE charges SET final_state = $2, metadata = metadata || $5::jsonb, updated_at = now()
			 WHERE id = $1 AND source = 'live' AND final_state = $3 AND ended_at = $4
			   AND final_state IN ('charging_active','charging_ready','charging_connecting','waiting_on_charger')`,
			r.ID, plan.NewState[r.ID], r.FinalState, r.EndedAt, m)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return 0, fmt.Errorf("closestale: row %s changed %d rows; rolling back", r.ID, k)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// Blocker is a reason a batch cannot be restored cleanly. Nothing is restored while any exists.
type Blocker struct {
	ChargeID uuid.UUID
	Reason   string
}

// ErrRestoreBlocked: some row of the batch cannot be restored; nothing was changed.
var ErrRestoreBlocked = errors.New("closestale: the batch cannot be restored cleanly; nothing changed")

// lockRestore locks every row of the batch (FOR UPDATE, ordered by id) in the caller's transaction and returns the batch's
// backup rows with the reasons any cannot be restored: the row's final_state is no longer the one the repair wrote, or a person
// has built on it since (a user price, place or location override, note, tag, place correction or receipt).
func lockRestore(ctx context.Context, tx *sql.Tx, batchID uuid.UUID) ([]uuid.UUID, []Blocker, error) {
	rs, err := tx.QueryContext(ctx, `SELECT charge_id FROM charges_capped_backup WHERE batch_id = $1 AND restored_at IS NULL ORDER BY charge_id`, batchID)
	if err != nil {
		return nil, nil, err
	}
	var ids []uuid.UUID
	for rs.Next() {
		var id uuid.UUID
		if err := rs.Scan(&id); err != nil {
			rs.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, nil, err
	}
	var blockers []Blocker
	for _, id := range ids {
		var st, wrote sql.NullString
		var user, restamp, receipt bool
		var edited bool
		err := tx.QueryRowContext(ctx, `
			SELECT c.final_state, c.metadata #>> '{`+MarkerKey+`,batch}',
			       COALESCE(c.cost_source,'') = 'user',
			       (c.place_override IS NOT NULL OR c.location_override IS NOT NULL OR COALESCE(c.notes,'') <> '' OR cardinality(c.tags) > 0),
			       EXISTS (SELECT 1 FROM charge_restamps r WHERE r.charge_id = c.id),
			       EXISTS (SELECT 1 FROM rivian_receipts rr WHERE rr.charge_id = c.id)
			  FROM charges c WHERE c.id = $1 FOR UPDATE`, id).Scan(&st, &wrote, &user, &edited, &restamp, &receipt)
		if errors.Is(err, sql.ErrNoRows) {
			blockers = append(blockers, Blocker{id, "the row is gone"})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		switch {
		case !wrote.Valid || wrote.String != batchID.String():
			blockers = append(blockers, Blocker{id, "the row no longer carries this batch's mark"})
		case IsOpenState(st.String):
			blockers = append(blockers, Blocker{id, "the row is open again"})
		case user || edited || restamp || receipt:
			blockers = append(blockers, Blocker{id, "a person has built on the row since the repair"})
		}
	}
	return ids, blockers, nil
}

// RestoreBlockers lists the reasons the batch cannot be restored, for the restore dry run. It writes nothing.
func RestoreBlockers(ctx context.Context, d *sql.DB, batchID uuid.UUID) ([]Blocker, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, blockers, err := lockRestore(ctx, tx, batchID)
	return blockers, err
}

// Restore undoes a batch in ONE transaction, all or nothing: every row is locked first; if any is blocked it returns
// ErrRestoreBlocked with nothing changed, and a statement that touches an unexpected number of rows rolls everything back. It puts
// back only what the repair changed: final_state (from the backup row) and the metadata with only the repair's own key removed.
// updated_at is touched by the table's trigger on every update (the old value stays in the backup row).
func Restore(ctx context.Context, d *sql.DB, batchID uuid.UUID) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, blockers, err := lockRestore(ctx, tx, batchID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("closestale: batch %s has nothing to restore (unknown or already restored)", batchID)
	}
	if len(blockers) > 0 {
		return 0, fmt.Errorf("%w: %d blocker(s), first: %s (%s)", ErrRestoreBlocked, len(blockers), blockers[0].ChargeID, blockers[0].Reason)
	}
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `
			UPDATE charges c
			   SET final_state = b.row->>'final_state', metadata = c.metadata - '`+MarkerKey+`'
			  FROM charges_capped_backup b
			 WHERE b.batch_id = $1 AND b.charge_id = $2 AND c.id = b.charge_id`, batchID, id)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return 0, fmt.Errorf("closestale: restoring %s changed %d rows; rolling back", id, k)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE charges_capped_backup SET restored_at = now() WHERE batch_id = $1 AND charge_id = $2`, batchID, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}
